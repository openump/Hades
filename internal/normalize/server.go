package normalize

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/delivery"
	"github.com/openump/Hades/internal/operations"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

type Server struct {
	qmu                                                                                 sync.Mutex
	quarantineFIFO                                                                      *list.List
	quarantineRecords, quarantineBytes                                                  int
	processingLatency                                                                   operations.Latency
	refusedBytes                                                                        uint64
	pendingSince                                                                        []time.Time
	capacityRefusals, storageFailures, processingCount, staleReadings, unknownFreshness uint64
	processingMS                                                                        float64
	lastAdmission                                                                       time.Time
	engine                                                                              *Engine
	mu                                                                                  sync.Mutex
	pending                                                                             [][]byte
	bytes, maxRecords, maxBytes                                                         int
	quarantine                                                                          *storage.Store
	admitted, published, rejected, evicted, retries                                     uint64
	wake                                                                                chan struct{}
	slots                                                                               chan struct{}
}

func NewServer(engine *Engine, dir string, maxRecords, maxBytes int) (*Server, error) {
	if maxRecords < 1 || maxRecords > 4096 || maxBytes < 1 || maxBytes > 16*1024*1024 {
		return nil, fmt.Errorf("normalizer limit exceeds delivery profile")
	}
	q, err := storage.Open(dir, 10000, 64*1024*1024, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	fifo := list.New()
	keys := make([]string, 0, len(q.Records))
	for key := range q.Records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fifo.PushBack(key)
	}
	return &Server{quarantineFIFO: fifo, quarantineRecords: len(q.Records), quarantineBytes: q.Bytes, engine: engine, maxRecords: maxRecords, maxBytes: maxBytes, quarantine: q, wake: make(chan struct{}, 1), slots: make(chan struct{}, 16)}, nil
}
func (s *Server) Close() error { return s.quarantine.Close() }

// Stats is a service-local snapshot; it does not imply transfer readiness.
func (s *Server) Stats() map[string]any {
	diskOK := s.quarantine.Available(0) == nil
	s.mu.Lock()
	defer s.mu.Unlock()
	oldestMS := int64(0)
	if len(s.pendingSince) > 0 {
		oldestMS = max(time.Since(s.pendingSince[0]).Milliseconds(), 0)
	}
	return map[string]any{"pending_records": len(s.pending), "pending_bytes": s.bytes, "admitted": s.admitted, "published": s.published, "rejected": s.rejected, "quarantine_records": s.quarantineRecords, "quarantine_bytes": s.quarantineBytes, "quarantine_evictions": s.evicted, "retries": s.retries, "publish_failures": s.retries, "capacity_refusals": s.capacityRefusals, "refused_bytes": s.refusedBytes, "storage_failures": s.storageFailures, "processing_count": s.processingCount, "processing_latency": s.processingLatency.Snapshot(), "processing_ms_sum": s.processingMS, "stale_readings": s.staleReadings, "unknown_freshness": s.unknownFreshness, "disk_reserve_ok": diskOK, "queue_limit_records": s.maxRecords, "queue_limit_bytes": s.maxBytes, "last_admission": s.lastAdmission, "oldest_pending_ms": oldestMS, "transfer_state": "not_observed_by_this_service"}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.Stats())
		return
	}
	if r.URL.Path != "/observations" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		s.mu.Lock()
		s.capacityRefusals++
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "busy", 503)
		return
	}
	started := time.Now()
	raw, err := io.ReadAll(io.LimitReader(r.Body, contracts.MaxPayloadBytes+1))
	if err != nil {
		http.Error(w, "body read failed", 400)
		return
	}
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.processingLatency.Observe(time.Since(started))
		s.processingCount++
		s.processingMS += float64(time.Since(started)) / float64(time.Millisecond)
	}()
	status := 422
	if len(raw) > contracts.MaxPayloadBytes {
		status = 413
		err = fmt.Errorf("payload exceeds 65536 bytes")
	} else {
		if _, err = contracts.Decode(raw); err != nil {
			status = 400
		}
	}
	var normalized []byte
	if err == nil {
		normalized, err = s.engine.Map(raw)
	}
	if err != nil {
		s.mu.Lock()
		s.rejected++
		s.mu.Unlock()
		reason := err.Error()
		if len(reason) > 512 {
			reason = reason[:512]
		}
		record, _ := json.Marshal(map[string]any{"received_at": time.Now().UTC(), "status": status, "reason": reason, "payload_prefix": raw[:min(len(raw), contracts.MaxPayloadBytes)], "truncated": len(raw) > contracts.MaxPayloadBytes})
		if qerr := s.quarantineRecord(record); qerr != nil {
			http.Error(w, "quarantine unavailable", 503)
			return
		}
		http.Error(w, reason, status)
		return
	}
	obj, _ := s.engine.Config.Validator.Validate("telemetry", normalized)
	observed, _ := time.Parse(time.RFC3339Nano, obj["observed_at"].(string))
	source := obj["source"].(map[string]any)
	var uncertainty *float64
	if source["clock_uncertainty_ms"] != nil {
		u := sample.Num(source["clock_uncertainty_ms"])
		uncertainty = &u
	}
	var stale, unknown uint64
	for name := range obj["measurements"].(map[string]any) {
		definition := s.engine.Config.Metadata["measurements"].(map[string]any)[name].(map[string]any)
		switch contracts.Freshness(observed, time.Now(), sample.Num(definition["expected_interval_ms"]), uncertainty) {
		case "stale":
			stale++
		case "unknown":
			unknown++
		}
	}
	s.mu.Lock()
	s.staleReadings += stale
	s.unknownFreshness += unknown
	charge := len(normalized) + storage.Overhead
	if len(s.pending) >= s.maxRecords || s.bytes+charge > s.maxBytes {
		s.capacityRefusals++
		s.refusedBytes += uint64(len(normalized))
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "volatile queue full", 503)
		return
	}
	s.pending = append(s.pending, normalized)
	s.pendingSince = append(s.pendingSince, time.Now())
	s.bytes += charge
	s.admitted++
	s.lastAdmission = time.Now().UTC()
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(202)
	io.WriteString(w, "{\"acceptance\":\"accepted_volatile\"}\n")
}

// quarantineRecord serializes only diagnostic ownership. Publication and health
// never acquire qmu; the queue snapshot is cached after each disk mutation.
func (s *Server) quarantineRecord(record []byte) (err error) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.quarantineRecords = len(s.quarantine.Records)
		s.quarantineBytes = s.quarantine.Bytes
		if err != nil {
			s.storageFailures++
		}
	}()
	for len(s.quarantine.Records) >= 10000 || s.quarantine.Bytes+len(record)+storage.Overhead+128 > 64*1024*1024 {
		front := s.quarantineFIFO.Front()
		if front == nil {
			break
		}
		if err = s.quarantine.Delete(front.Value.(string)); err != nil {
			return err
		}
		s.quarantineFIFO.Remove(front)
		s.mu.Lock()
		s.evicted++
		s.mu.Unlock()
	}
	key := fmt.Sprintf("%020d-%s", time.Now().UnixNano(), sample.UUID())
	if err = s.quarantine.Put(key, record); err != nil {
		return err
	}
	s.quarantineFIFO.PushBack(key)
	return nil
}
func (s *Server) Run(ctx context.Context, publish func(context.Context, []byte) error) error {
	for {
		s.mu.Lock()
		var raw []byte
		if len(s.pending) > 0 {
			raw = s.pending[0]
		}
		s.mu.Unlock()
		if raw == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-s.wake:
				continue
			}
		}
		for attempt := 0; ; attempt++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := publish(ctx, append([]byte(nil), raw...)); err == nil {
				break
			} else {
				log.Printf("normalizer publication retry: %v", err)
			}
			s.mu.Lock()
			s.retries++
			s.mu.Unlock()
			if err := delivery.Pause(ctx, attempt); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.pending[0] = nil // Release the retired payload even while backing storage is reused.
		s.pending = s.pending[1:]
		s.pendingSince = s.pendingSince[1:]
		s.bytes -= len(raw) + storage.Overhead
		s.published++
		s.mu.Unlock()
	}
}

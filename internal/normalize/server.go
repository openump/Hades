package normalize

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/delivery"
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
	engine                                          *Engine
	mu                                              sync.Mutex
	pending                                         [][]byte
	bytes, maxRecords, maxBytes                     int
	quarantine                                      *storage.Store
	admitted, published, rejected, evicted, retries uint64
	wake                                            chan struct{}
	slots                                           chan struct{}
}

func NewServer(engine *Engine, dir string, maxRecords, maxBytes int) (*Server, error) {
	if maxRecords < 1 || maxRecords > 4096 || maxBytes < 1 || maxBytes > 16*1024*1024 {
		return nil, fmt.Errorf("normalizer limit exceeds delivery profile")
	}
	q, err := storage.Open(dir, 10000, 64*1024*1024, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	return &Server{engine: engine, maxRecords: maxRecords, maxBytes: maxBytes, quarantine: q, wake: make(chan struct{}, 1), slots: make(chan struct{}, 16)}, nil
}
func (s *Server) Close() error { return s.quarantine.Close() }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"pending_records": len(s.pending), "pending_bytes": s.bytes, "admitted": s.admitted, "published": s.published, "rejected": s.rejected, "quarantine_records": len(s.quarantine.Records), "quarantine_bytes": s.quarantine.Bytes, "quarantine_evictions": s.evicted, "retries": s.retries})
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
		w.Header().Set("Retry-After", "1")
		http.Error(w, "busy", 503)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, contracts.MaxPayloadBytes+1))
	if err != nil {
		http.Error(w, "body read failed", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
		s.rejected++
		reason := err.Error()
		if len(reason) > 512 {
			reason = reason[:512]
		}
		record, _ := json.Marshal(map[string]any{"received_at": time.Now().UTC(), "status": status, "reason": reason, "payload_prefix": raw[:min(len(raw), contracts.MaxPayloadBytes)], "truncated": len(raw) > contracts.MaxPayloadBytes})
		key := fmt.Sprintf("%020d-%s", time.Now().UnixNano(), sample.UUID())
		// Diagnostic eviction is allowed; normal publication records are never evicted.
		for len(s.quarantine.Records) >= 10000 || s.quarantine.Bytes+len(record)+storage.Overhead+128 > 64*1024*1024 {
			keys := make([]string, 0, len(s.quarantine.Records))
			for k := range s.quarantine.Records {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if len(keys) == 0 {
				break
			}
			if qerr := s.quarantine.Delete(keys[0]); qerr != nil {
				http.Error(w, "quarantine storage failed", 503)
				return
			}
			s.evicted++
		}
		if qerr := s.quarantine.Put(key, record); qerr != nil {
			log.Printf("quarantine unavailable: %v", qerr)
			http.Error(w, "quarantine unavailable", 503)
			return
		}
		http.Error(w, reason, status)
		return
	}
	charge := len(normalized) + storage.Overhead
	if len(s.pending) >= s.maxRecords || s.bytes+charge > s.maxBytes {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "volatile queue full", 503)
		return
	}
	s.pending = append(s.pending, normalized)
	s.bytes += charge
	s.admitted++
	select {
	case s.wake <- struct{}{}:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(202)
	io.WriteString(w, "{\"acceptance\":\"accepted_volatile\"}\n")
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
		s.pending = s.pending[1:]
		s.bytes -= len(raw) + storage.Overhead
		s.published++
		s.mu.Unlock()
	}
}

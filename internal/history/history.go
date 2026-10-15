// Package history owns synced intake and best-effort VictoriaMetrics API delivery.
package history

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/delivery"
	"github.com/openump/Hades/internal/operations"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
)

type StorageError struct{ error }

func isStorageFailure(err error) bool { var failure *StorageError; return errors.As(err, &failure) }

var ErrConflict = errors.New("immutable identity or series/timestamp conflict")

type Record struct {
	Reason   string    `json:"suspended_reason,omitempty"`
	Topic    string    `json:"topic"`
	Payload  []byte    `json:"payload"`
	Digest   string    `json:"digest"`
	State    string    `json:"state"`
	Accepted time.Time `json:"accepted_at"`
	Retired  time.Time `json:"retired_at"`
	Lines    []byte    `json:"lines"`
	Keys     []string  `json:"keys"`
	Skips    int       `json:"skips"`
}
type Adapter struct {
	drainMu                                                     sync.Mutex
	retryAt                                                     time.Time
	retryAttempt                                                int
	batchRecords, batchBytes                                    int
	writeLatency                                                operations.Latency
	validationFailures, storageFailures, capacityRefusals       uint64
	writeCount                                                  uint64
	writeMS                                                     float64
	Diagnostic                                                  func(string, []byte, error) error
	pending                                                     map[string]bool
	nextCleanup                                                 time.Time
	closed                                                      bool
	cfg                                                         *sample.Config
	mu                                                          sync.Mutex
	store                                                       *storage.Store
	records                                                     map[string]Record
	samples                                                     map[string]string
	retries, duplicates, conflicts, skipped, admitted, accepted uint64
}

func Open(cfg *sample.Config, dir string) (*Adapter, error) {
	store, err := storage.Open(dir, 100000, 256*1024*1024, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	a := &Adapter{batchRecords: 64, batchBytes: 1024 * 1024, cfg: cfg, store: store, records: map[string]Record{}, samples: map[string]string{}, pending: map[string]bool{}}
	for key, raw := range store.Records {
		var r Record
		err = json.Unmarshal(raw, &r)
		if err != nil || (r.State != "pending" && r.State != "api_accepted" && r.State != "suspended") || r.Accepted.IsZero() || (r.State == "api_accepted" && r.Retired.IsZero()) {
			store.Close()
			return nil, fmt.Errorf("invalid intake record")
		}
		p, err := a.validate(r.Topic, r.Payload)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("intake context unavailable: %w", err)
		}
		lines, keys, skips, err := Map(cfg, p)
		if err != nil || key != identity(p) || r.Digest != digest(r.Topic, r.Payload) || string(lines) != string(r.Lines) || skips != r.Skips || !equalKeys(keys, r.Keys) {
			store.Close()
			return nil, fmt.Errorf("intake digest/mapping mismatch")
		}
		a.records[key] = r
		if r.State == "pending" {
			a.pending[key] = true
		}
		for _, sampleKey := range keys {
			if _, exists := a.samples[sampleKey]; exists {
				store.Close()
				return nil, ErrConflict
			}
			a.samples[sampleKey] = key
		}
	}
	return a, nil
}
func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func digest(topic string, raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(topic+"\x00"), raw...)))
}
func identity(p map[string]any) string {
	return p["asset_id"].(string) + "/" + p["message_id"].(string)
}
func (a *Adapter) validate(topic string, raw []byte) (map[string]any, error) {
	if topic != a.cfg.Prefix+"telemetry" {
		return nil, fmt.Errorf("unexpected telemetry topic")
	}
	p, err := a.cfg.Validator.Validate("telemetry", raw)
	if err != nil {
		return nil, err
	}
	if err = a.cfg.Registry.ValidateTopic(topic, p["asset_id"].(string)); err != nil {
		return nil, err
	}
	if err = contracts.ValidateContext(p, a.cfg.Metadata); err != nil {
		return nil, err
	}
	return p, nil
}
func (a *Adapter) Intake(topic string, raw []byte) (string, error) {
	p, err := a.validate(topic, raw)
	if err != nil {
		a.mu.Lock()
		a.validationFailures++
		a.mu.Unlock()
		return "", err
	}
	lines, keys, skips, err := Map(a.cfg, p)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", &StorageError{context.Canceled}
	}
	key := identity(p)
	hash := digest(topic, raw)
	if old, exists := a.records[key]; exists {
		if old.Digest != hash {
			a.conflicts++
			return "", ErrConflict
		}
		a.duplicates++
		return "duplicate", nil
	}
	// Only completed dedup records may expire. Pending/suspended ownership has no
	// automatic age expiry, even when the historian remains unavailable for days.
	now := time.Now().UTC()
	if now.After(a.nextCleanup) {
		for id, r := range a.records {
			if r.State == "api_accepted" && now.Sub(r.Retired) > 7*24*time.Hour {
				if err = a.store.Delete(id); err != nil {
					return "", &StorageError{err}
				}
				for _, k := range r.Keys {
					delete(a.samples, k)
				}
				delete(a.records, id)
			}
		}

		a.nextCleanup = now.Add(time.Minute)
	}
	for _, sampleKey := range keys {
		if _, exists := a.samples[sampleKey]; exists {
			a.conflicts++
			return "", ErrConflict
		}
	}
	r := Record{Topic: topic, Payload: append([]byte(nil), raw...), Digest: hash, State: "pending", Accepted: now, Lines: lines, Keys: keys, Skips: skips}
	encoded, _ := json.Marshal(r)
	if err = a.store.Put(key, encoded); err != nil {
		a.storageFailures++
		if errors.Is(err, storage.ErrFull) {
			a.capacityRefusals++
		}
		return "", &StorageError{err}
	}
	a.records[key] = r
	a.pending[key] = true
	for _, sampleKey := range keys {
		a.samples[sampleKey] = key
	}
	a.skipped += uint64(skips)
	a.admitted++
	return "accepted_synced", nil
}

type PermanentError struct {
	Status int
	Reason string
}

func (e *PermanentError) Error() string {
	if e.Reason != "" {
		return e.Reason + ": intake suspended"
	}
	return fmt.Sprintf("historian HTTP %d: intake suspended", e.Status)
}

// ConfigureDrain bounds each request and each drain invocation.
func (a *Adapter) ConfigureDrain(records, bytes int) error {
	if records < 1 || records > 256 || bytes < 65536 || bytes > 1024*1024 {
		return fmt.Errorf("batch limits: records 1..256, bytes 65536..1048576")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.batchRecords = records
	a.batchBytes = bytes
	return nil
}

// Drain makes at most one bounded HTTP request. Transient failures retain all
// ownership and schedule a future attempt; callers keep polling without sleeps.
// Ambiguous remote acceptance can replay samples (at-least-once API delivery).
func (a *Adapter) Drain(ctx context.Context, write func(context.Context, []byte) error) error {
	a.drainMu.Lock()
	defer a.drainMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.mu.Lock()
	if time.Now().Before(a.retryAt) {
		a.mu.Unlock()
		return nil
	}
	ids := make([]string, 0, len(a.pending))
	for id := range a.pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		x, y := a.records[ids[i]], a.records[ids[j]]
		if x.Accepted.Equal(y.Accepted) {
			return ids[i] < ids[j]
		}
		return x.Accepted.Before(y.Accepted)
	})
	var body []byte
	selected := make([]string, 0, a.batchRecords)
	records := make([]Record, 0, a.batchRecords)
	var invalid error
	for _, id := range ids {
		r := a.records[id]
		if len(selected) >= a.batchRecords || len(body)+len(r.Lines) > a.batchBytes {
			break
		}
		window := WindowCheck(r.Payload, time.Now())
		if window != nil {
			if len(selected) > 0 {
				break
			}
			invalid = window
		}
		selected = append(selected, id)
		records = append(records, r)
		body = append(body, r.Lines...)
		if invalid != nil {
			break
		}
	}
	a.mu.Unlock()
	if len(selected) == 0 {
		if len(ids) > 0 {
			return fmt.Errorf("historian record exceeds configured batch bytes")
		}
		return nil
	}
	err := invalid
	if err == nil {
		started := time.Now()
		err = write(ctx, body)
		a.mu.Lock()
		a.writeLatency.Observe(time.Since(started))
		a.writeCount++
		a.writeMS += float64(time.Since(started)) / float64(time.Millisecond)
		a.mu.Unlock()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var permanent *PermanentError
	if err != nil && !errors.As(err, &permanent) {
		a.mu.Lock()
		a.retries++
		a.retryAt = time.Now().Add(max(time.Millisecond, delivery.Delay(a.retryAttempt)))
		a.retryAttempt++
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	a.retryAt = time.Time{}
	a.retryAttempt = 0
	a.mu.Unlock()
	for i, r := range records {
		if err == nil {
			r.State = "api_accepted"
			r.Retired = time.Now().UTC()
		} else {
			r.State = "suspended"
			r.Reason = err.Error()
		}
		a.mu.Lock()
		encoded, _ := json.Marshal(r)
		storageErr := a.store.Put(selected[i], encoded)
		if storageErr == nil {
			a.records[selected[i]] = r
			delete(a.pending, selected[i])
			if r.State == "api_accepted" {
				a.accepted++
			}
		} else {
			a.storageFailures++
		}
		a.mu.Unlock()
		if storageErr != nil {
			return storageErr
		}
		if r.State == "suspended" && a.Diagnostic != nil {
			if diagnosticErr := a.Diagnostic(r.Topic, r.Payload, fmt.Errorf("%s", r.Reason)); diagnosticErr != nil {
				return diagnosticErr
			}
		}
	}
	return nil
}
func (a *Adapter) Stats() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending, suspended := 0, 0
	oldestMS := int64(0)
	for _, r := range a.records {
		if r.State == "pending" {
			pending++
			oldestMS = max(oldestMS, time.Since(r.Accepted).Milliseconds())
		}
		if r.State == "suspended" {
			suspended++
		}
	}
	return map[string]any{"batch_limit_records": a.batchRecords, "batch_limit_bytes": a.batchBytes, "retry_at": a.retryAt, "records": len(a.records), "bytes": a.store.Bytes, "pending": pending, "oldest_pending_ms": oldestMS, "suspended": suspended, "admitted": a.admitted, "api_accepted": a.accepted, "duplicates": a.duplicates, "conflicts": a.conflicts, "skipped_values": a.skipped, "retries": a.retries, "validation_failures": a.validationFailures, "storage_failures": a.storageFailures, "capacity_refusals": a.capacityRefusals, "write_count": a.writeCount, "write_latency": a.writeLatency.Snapshot(), "write_ms_sum": a.writeMS, "storage": a.store.Stats(), "transfer_state": "not_observed_by_this_service"}
}
func (a *Adapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return a.store.Close()
}

// Resume changes only ownership state; original topic/body and digest stay fixed.
func (a *Adapter) Resume(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := a.cfg.Metadata["asset_id"].(string) + "/" + id
	r, exists := a.records[key]
	if !exists || r.State != "suspended" {
		return fmt.Errorf("message is not suspended")
	}
	r.State = "pending"
	raw, _ := json.Marshal(r)
	if err := a.store.Put(key, raw); err != nil {
		return err
	}
	a.records[key] = r
	a.pending[key] = true
	return nil
}

// WindowCheck matches the reference historian's one-day retention. Invalid time
// never silently retires through an API that can discard out-of-window samples.
func WindowCheck(raw []byte, now time.Time) error {
	p, err := contracts.Decode(raw)
	if err != nil {
		return err
	}
	observed, err := time.Parse(time.RFC3339Nano, p.(map[string]any)["observed_at"].(string))
	if err != nil {
		return err
	}
	if observed.Before(now.Add(-24*time.Hour)) || observed.After(now.Add(time.Minute)) {
		return &PermanentError{Reason: "observation outside one-day retention or one-minute future allowance"}
	}
	return nil
}

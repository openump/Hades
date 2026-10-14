// Package history owns synced intake and best-effort VictoriaMetrics API delivery.
package history

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/delivery"
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
	a := &Adapter{cfg: cfg, store: store, records: map[string]Record{}, samples: map[string]string{}, pending: map[string]bool{}}
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

// Drain retries each pending record until API acceptance; callers can continue
// intake concurrently. Permanent errors suspend ownership instead of deletion.
func (a *Adapter) Drain(ctx context.Context, write func(context.Context, []byte) error) error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.pending))
	for id := range a.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	a.mu.Unlock()
	for _, id := range ids {
		a.mu.Lock()
		r := a.records[id]
		a.mu.Unlock()
		for attempt := 0; ; attempt++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err := WindowCheck(r.Payload, time.Now())
			if err == nil {
				started := time.Now()
				err = write(ctx, append([]byte(nil), r.Lines...))
				a.mu.Lock()
				a.writeCount++
				a.writeMS += float64(time.Since(started)) / float64(time.Millisecond)
				a.mu.Unlock()
			}
			if err == nil {
				r.State = "api_accepted"
				r.Retired = time.Now().UTC()
			} else {
				var permanent *PermanentError
				if errors.As(err, &permanent) {
					r.State = "suspended"
					r.Reason = err.Error()
					log.Print(err)
				} else {
					a.mu.Lock()
					a.retries++
					a.mu.Unlock()
					log.Printf("historian retry: %v", err)
					if err = delivery.Pause(ctx, attempt); err != nil {
						return err
					}
					continue
				}
			}
			a.mu.Lock()
			encoded, _ := json.Marshal(r)
			err = a.store.Put(id, encoded)
			if err == nil {
				a.records[id] = r
				delete(a.pending, id)
				if r.State == "api_accepted" {
					a.accepted++
				}
			}
			a.mu.Unlock()
			if err != nil {
				return err
			}
			if r.State == "suspended" && a.Diagnostic != nil {
				if err = a.Diagnostic(r.Topic, r.Payload, fmt.Errorf("%s", r.Reason)); err != nil {
					return err
				}
			}
			break
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
	return map[string]any{"records": len(a.records), "bytes": a.store.Bytes, "pending": pending, "oldest_pending_ms": oldestMS, "suspended": suspended, "admitted": a.admitted, "api_accepted": a.accepted, "duplicates": a.duplicates, "conflicts": a.conflicts, "skipped_values": a.skipped, "retries": a.retries, "validation_failures": a.validationFailures, "storage_failures": a.storageFailures, "capacity_refusals": a.capacityRefusals, "write_count": a.writeCount, "write_ms_sum": a.writeMS, "storage": a.store.Stats(), "transfer_state": "not_implemented"}
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

// Package transfer owns an independent, bounded cloud queue. A verified durable
// receipt is the only successful retirement boundary; local MQTT is separate.
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/delivery"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
)

const Profile = "openump-transfer/1"

type StorageError struct{ error }

func (e *StorageError) Unwrap() error { return e.error }

func isStorageError(err error) bool { var e *StorageError; return errors.As(err, &e) }

var ErrConflict = errors.New("transfer identity conflict")
var ErrReceipt = errors.New("invalid durable transfer receipt")
var ErrAuthorization = errors.New("transfer authorization paused; repair credentials and restart")

type PermanentError struct{ Status int }

func (e *PermanentError) Error() string { return fmt.Sprintf("transfer HTTP %d: suspended", e.Status) }

type Record struct {
	Topic    string    `json:"topic"`
	Payload  []byte    `json:"payload"`
	ID       string    `json:"message_id"`
	Digest   string    `json:"digest"`
	Accepted time.Time `json:"queued_at"`
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
}
type Queue struct {
	mu                                                                                       sync.Mutex
	cfg                                                                                      *sample.Config
	store                                                                                    *storage.Store
	records                                                                                  map[string]Record
	closed, paused                                                                           bool
	admitted, delivered, duplicates, conflicts, drops, retries, receiptErrors, storageErrors uint64
}

func Digest(topic string, raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(topic+"\x00"), raw...)))
}
func Enterprise(cfg *sample.Config) string { return strings.Split(cfg.Prefix, "/")[2] }

// Validate uses the pinned approved context; unknown revisions fail closed.
func Validate(cfg *sample.Config, topic string, raw []byte) (map[string]any, error) {
	if !strings.HasPrefix(topic, cfg.Prefix) {
		return nil, fmt.Errorf("unapproved topic")
	}
	kind := strings.TrimPrefix(topic, cfg.Prefix)
	if kind != "telemetry" && kind != "metadata" && kind != "status" {
		return nil, fmt.Errorf("unapproved message kind")
	}
	p, err := cfg.Validator.Validate(kind, raw)
	if err != nil {
		return nil, err
	}
	if err = cfg.Registry.ValidateTopic(topic, p["asset_id"].(string)); err != nil {
		return nil, err
	}
	if p["metadata_revision"] != cfg.Metadata["metadata_revision"] {
		return nil, fmt.Errorf("unapproved metadata revision")
	}
	if kind == "metadata" && !reflect.DeepEqual(p, cfg.Metadata) {
		return nil, fmt.Errorf("metadata snapshot conflict")
	}
	if kind == "telemetry" {
		err = contracts.ValidateContext(p, cfg.Metadata)
	}
	return p, err
}
func Open(cfg *sample.Config, dir string, maxRecords, maxBytes int) (*Queue, error) {
	if maxRecords > 100000 || maxBytes > 256*1024*1024 {
		return nil, fmt.Errorf("limits exceed transfer profile")
	}
	s, err := storage.Open(dir, maxRecords, maxBytes, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	q := &Queue{cfg: cfg, store: s, records: map[string]Record{}}
	for id, raw := range s.Records {
		var r Record
		if err = json.Unmarshal(raw, &r); err == nil {
			var p map[string]any
			p, err = Validate(cfg, r.Topic, r.Payload)
			if err == nil && (r.ID != id || p["message_id"] != id || r.Digest != Digest(r.Topic, r.Payload) || r.Accepted.IsZero() || (r.State != "pending" && r.State != "suspended")) {
				err = fmt.Errorf("corrupt transfer record")
			}
		}
		if err != nil {
			s.Close()
			return nil, err
		}
		q.records[id] = r
	}
	return q, nil
}
func (q *Queue) Intake(topic string, raw []byte) error {
	p, err := Validate(q.cfg, topic, raw)
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return context.Canceled
	}
	id := p["message_id"].(string)
	hash := Digest(topic, raw)
	if old, ok := q.records[id]; ok {
		if old.Digest != hash {
			q.conflicts++
			return ErrConflict
		}
		q.duplicates++
		return nil
	}
	r := Record{Topic: topic, Payload: append([]byte(nil), raw...), ID: id, Digest: hash, Accepted: time.Now().UTC(), State: "pending"}
	encoded, _ := json.Marshal(r)
	if err = q.store.Put(id, encoded); err != nil {
		if errors.Is(err, storage.ErrFull) {
			q.drops++
		} else {
			q.storageErrors++
		}
		return &StorageError{err}
	}
	q.records[id] = r
	q.admitted++
	return nil
}
func (q *Queue) change(r Record) error {
	raw, _ := json.Marshal(r)
	if err := q.store.Put(r.ID, raw); err != nil {
		q.storageErrors++
		return err
	}
	q.records[r.ID] = r
	return nil
}

// Drain has one caller/one request in flight. Authentication failures pause the
// entire worker, permanent errors preserve suspended records. Never expire a
// source clock of unknown quality. Replay beyond 72h of local durable ownership
// is suspended for explicit receiver reconciliation, rather than silently lost.
func (q *Queue) Drain(ctx context.Context, send func(context.Context, Record) error) error {
	q.mu.Lock()
	if q.paused {
		q.mu.Unlock()
		return ErrAuthorization
	}
	ids := make([]string, 0, len(q.records))
	for id, r := range q.records {
		if r.State == "pending" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return q.records[ids[i]].Accepted.Before(q.records[ids[j]].Accepted) })
	q.mu.Unlock()
	for _, id := range ids {
		q.mu.Lock()
		r := q.records[id]
		q.mu.Unlock()
		for attempt := 0; ; attempt++ {
			if time.Since(r.Accepted) > 72*time.Hour {
				q.mu.Lock()
				r.State = "suspended"
				r.Reason = "replay_window_requires_reconciliation"
				err := q.change(r)
				q.mu.Unlock()
				if err != nil {
					return err
				}
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err := send(ctx, r)
			q.mu.Lock()
			if err == nil {
				err = q.store.Delete(id)
				if err == nil {
					delete(q.records, id)
					q.delivered++
				} else {
					q.storageErrors++
				}
				q.mu.Unlock()
				if err != nil {
					return err
				}
				break
			}
			if errors.Is(err, ErrAuthorization) {
				q.paused = true
				q.mu.Unlock()
				return err
			}
			var permanent *PermanentError
			if errors.As(err, &permanent) {
				r.State = "suspended"
				r.Reason = permanent.Error()
				err = q.change(r)
				q.mu.Unlock()
				if err != nil {
					return err
				}
				break
			}
			q.retries++
			if errors.Is(err, ErrReceipt) {
				q.receiptErrors++
			}
			q.mu.Unlock()
			delay := delivery.Delay(attempt)
			var hint *RetryError
			if errors.As(err, &hint) && hint.After > delay {
				delay = hint.After
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}

// Resume requires operator reconciliation. Restart supplies a new 72h replay
// budget but never changes the original topic, identity, digest or event time.
func (q *Queue) Resume(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	r, ok := q.records[id]
	if !ok || r.State != "suspended" {
		return fmt.Errorf("no suspended transfer identity")
	}
	r.State = "pending"
	r.Reason = ""
	r.Accepted = time.Now().UTC()
	return q.change(r)
}
func (q *Queue) Stats() map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending, suspended := 0, 0
	var oldest time.Time
	for _, r := range q.records {
		if r.State == "pending" {
			pending++
		} else {
			suspended++
		}
		if oldest.IsZero() || r.Accepted.Before(oldest) {
			oldest = r.Accepted
		}
	}
	age := int64(0)
	if !oldest.IsZero() {
		age = max(0, time.Since(oldest).Milliseconds())
	}
	return map[string]any{"pending": pending, "suspended": suspended, "oldest_pending_ms": age, "admitted": q.admitted, "delivered": q.delivered, "duplicates": q.duplicates, "conflicts": q.conflicts, "drops": q.drops, "retries": q.retries, "receipt_errors": q.receiptErrors, "storage_errors": q.storageErrors, "authorization_paused": q.paused, "store": q.store.Stats(), "boundary": "verified_durable_receipt", "counters": "process_lifetime"}
}
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	return q.store.Close()
}

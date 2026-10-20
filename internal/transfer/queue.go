// Package transfer owns an independent, bounded cloud queue. A verified durable
// receipt is the only successful retirement boundary; local MQTT is separate.
package transfer

import (
	"context"
	"crypto/ed25519"
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
	receiptKeys                                                                                            map[string]ed25519.PublicKey
	drainMu                                                                                                sync.Mutex
	retryAt                                                                                                time.Time
	retryAttempt                                                                                           int
	now                                                                                                    func() time.Time
	mu                                                                                                     sync.Mutex
	cfg                                                                                                    *sample.Config
	store                                                                                                  *storage.Store
	records                                                                                                map[string]Record
	closed, paused                                                                                         bool
	admitted, delivered, duplicates, conflicts, drops, droppedBytes, retries, receiptErrors, storageErrors uint64
}

func (q *Queue) loadRecord(id string, cached Record) (Record, error) {
	if len(cached.Payload) != 0 {
		return cached, nil
	}
	raw, ok, err := q.store.Read(id)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("corrupt transfer record")
		}
		return Record{}, err
	}
	var full Record
	if err = json.Unmarshal(raw, &full); err != nil {
		return Record{}, err
	}
	return full, nil
}

func mergeRecord(base, overlay Record) Record {
	if overlay.Topic != "" {
		base.Topic = overlay.Topic
	}
	if len(overlay.Payload) != 0 {
		base.Payload = overlay.Payload
	}
	if overlay.ID != "" {
		base.ID = overlay.ID
	}
	if overlay.Digest != "" {
		base.Digest = overlay.Digest
	}
	if !overlay.Accepted.IsZero() {
		base.Accepted = overlay.Accepted
	}
	if overlay.State != "" {
		base.State = overlay.State
	}
	if overlay.Reason != "" {
		base.Reason = overlay.Reason
	}
	return base
}

func Digest(topic string, raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(topic+"\x00"), raw...)))
}
func Enterprise(cfg *sample.Config) string { return strings.Split(cfg.Prefix, "/")[2] }

// Validate uses the pinned approved context; unknown revisions fail closed.
func Validate(cfg *sample.Config, topic string, raw []byte) (map[string]any, error) {
	assetCfg, kind, err := cfg.ResolveTopic(topic)
	if err != nil {
		return nil, err
	}
	p, err := cfg.Validator.Validate(kind, raw)
	if err != nil {
		return nil, err
	}
	if err = cfg.Registry.ValidateTopic(topic, p["asset_id"].(string)); err != nil {
		return nil, err
	}
	if p["metadata_revision"] != assetCfg.Metadata["metadata_revision"] {
		return nil, fmt.Errorf("unapproved metadata revision")
	}
	if kind == "metadata" && !reflect.DeepEqual(p, assetCfg.Metadata) {
		return nil, fmt.Errorf("metadata snapshot conflict")
	}
	if kind == "telemetry" {
		err = contracts.ValidateContext(p, assetCfg.Metadata)
	}
	return p, err
}
func Open(cfg *sample.Config, dir string, maxRecords, maxBytes int) (*Queue, error) {
	return OpenWithReceiptKeys(cfg, dir, maxRecords, maxBytes, nil)
}
func OpenWithReceiptKeys(cfg *sample.Config, dir string, maxRecords, maxBytes int, keys map[string]ed25519.PublicKey) (*Queue, error) {
	if maxRecords > 100000 || maxBytes > 256*1024*1024 {
		return nil, fmt.Errorf("limits exceed transfer profile")
	}
	s, err := storage.Open(dir, maxRecords, maxBytes, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	q := &Queue{receiptKeys: keys, now: time.Now, cfg: cfg, store: s, records: map[string]Record{}}
	for id := range s.Records {
		raw, ok, err := s.Read(id)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("corrupt transfer record")
			}
			s.Close()
			return nil, err
		}
		var r Record
		if err = json.Unmarshal(raw, &r); err == nil {
			var p map[string]any
			p, err = ValidateWithReceiptKeys(cfg, r.Topic, r.Payload, keys)
			if err == nil && (r.ID != id || p["message_id"] != id || r.Digest != Digest(r.Topic, r.Payload) || r.Accepted.IsZero() || (r.State != "pending" && r.State != "suspended")) {
				err = fmt.Errorf("corrupt transfer record")
			}
		}
		if err != nil {
			s.Close()
			return nil, err
		}
		r.Payload = nil
		q.records[id] = r
	}
	return q, nil
}
func (q *Queue) Intake(topic string, raw []byte) error {
	p, err := ValidateWithReceiptKeys(q.cfg, topic, raw, q.receiptKeys)
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
			q.droppedBytes += uint64(len(raw))
		} else {
			q.storageErrors++
		}
		return &StorageError{err}
	}
	r.Payload = nil
	q.records[id] = r
	q.admitted++
	return nil
}
func (q *Queue) change(r Record) error {
	full := r
	if len(full.Payload) == 0 {
		stored, err := q.loadRecord(r.ID, r)
		if err != nil {
			return err
		}
		full = mergeRecord(stored, r)
	}
	raw, _ := json.Marshal(full)
	if err := q.store.Put(r.ID, raw); err != nil {
		q.storageErrors++
		return err
	}
	full.Payload = nil
	q.records[r.ID] = full
	return nil
}

// Drain has one caller/one request in flight. Authentication failures pause the
// entire worker, permanent errors preserve suspended records. Never expire a
// source clock of unknown quality. Replay beyond 72h of local durable ownership
// is suspended for explicit receiver reconciliation, rather than silently lost.
func (q *Queue) Drain(ctx context.Context, send func(context.Context, Record) error) error {
	q.drainMu.Lock()
	defer q.drainMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	q.mu.Lock()
	if q.now().Before(q.retryAt) {
		q.mu.Unlock()
		return nil
	}
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
	sort.Slice(ids, func(i, j int) bool {
		x, y := q.records[ids[i]], q.records[ids[j]]
		if x.Accepted.Equal(y.Accepted) {
			return ids[i] < ids[j]
		}
		return x.Accepted.Before(y.Accepted)
	})
	q.mu.Unlock()
	if len(ids) > 256 {
		ids = ids[:256]
	}
	for _, id := range ids {
		q.mu.Lock()
		r := q.records[id]
		full, err := q.loadRecord(id, r)
		q.mu.Unlock()
		if err != nil {
			return err
		}
		if q.now().Sub(r.Accepted) > 72*time.Hour {
			q.mu.Lock()
			full.State = "suspended"
			full.Reason = "replay_window_requires_reconciliation"
			err := q.change(full)
			q.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = send(ctx, full)
		q.mu.Lock()
		if err == nil {
			err = q.store.Delete(id)
			if err == nil {
				delete(q.records, id)
				q.delivered++
				q.retryAttempt = 0
				q.retryAt = time.Time{}
			} else {
				q.storageErrors++
			}
			q.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, ErrAuthorization) {
			q.paused = true
			q.mu.Unlock()
			return err
		}
		var permanent *PermanentError
		if errors.As(err, &permanent) {
			full.State = "suspended"
			full.Reason = permanent.Error()
			err = q.change(full)
			q.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		q.retries++
		if errors.Is(err, ErrReceipt) {
			q.receiptErrors++
		}
		if ctx.Err() != nil {
			q.mu.Unlock()
			return ctx.Err()
		}
		delay := max(time.Millisecond, delivery.Delay(q.retryAttempt))
		q.retryAttempt++
		var hint *RetryError
		if errors.As(err, &hint) && hint.After > delay {
			delay = hint.After
		}
		q.retryAt = q.now().Add(delay)
		q.mu.Unlock()
		return nil
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
	full, err := q.loadRecord(id, r)
	if err != nil {
		return err
	}
	full.State = "pending"
	full.Reason = ""
	full.Accepted = time.Now().UTC()
	return q.change(full)
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
	return map[string]any{"retry_at": q.retryAt, "pending": pending, "suspended": suspended, "oldest_pending_ms": age, "admitted": q.admitted, "delivered": q.delivered, "duplicates": q.duplicates, "conflicts": q.conflicts, "drops": q.drops, "dropped_bytes": q.droppedBytes, "retries": q.retries, "receipt_errors": q.receiptErrors, "storage_errors": q.storageErrors, "authorization_paused": q.paused, "store": q.store.Stats(), "boundary": "verified_durable_receipt", "counters": "process_lifetime"}
}
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	return q.store.Close()
}

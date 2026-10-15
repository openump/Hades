package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
)

func fixture(t *testing.T) (*sample.Config, Record) {
	t.Helper()
	cfg, err := sample.Load("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cfg.Observation("normal", 1, sample.UUID(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	json.Unmarshal(raw, &p)
	return cfg, Record{Topic: cfg.Prefix + "telemetry", Payload: raw, ID: p["message_id"].(string), Digest: Digest(cfg.Prefix+"telemetry", raw)}
}
func TestSyncedOwnershipAndReceiptBoundary(t *testing.T) {
	cfg, r := fixture(t)
	dir := t.TempDir() + "/queue"
	q, err := Open(cfg, dir, 2, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Intake(r.Topic, r.Payload); err != nil {
		t.Fatal(err)
	}
	q.Close()
	q, err = Open(cfg, dir, 2, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	send := func(_ context.Context, got Record) error {
		calls++
		if string(got.Payload) != string(r.Payload) || got.Digest != r.Digest {
			t.Fatal("immutable content changed")
		}
		if calls == 1 {
			return ErrReceipt
		}
		return nil
	}
	err = q.Drain(ctx, send)
	if err != nil || calls != 1 {
		t.Fatal("first attempt did not return", err, calls)
	}
	if err = q.Drain(ctx, send); err != nil || calls != 1 {
		t.Fatal("retry ignored backoff", err, calls)
	}
	q.retryAt = time.Time{}
	err = q.Drain(ctx, send)
	if err != nil || calls != 2 || q.Stats()["pending"] != 0 || q.Stats()["receipt_errors"] != uint64(1) {
		t.Fatal(err, calls, q.Stats())
	}
}
func TestBoundsAndSuspendedOwnership(t *testing.T) {
	cfg, r := fixture(t)
	q, err := Open(cfg, t.TempDir()+"/queue", 1, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err = q.Intake(r.Topic, r.Payload); err != nil {
		t.Fatal(err)
	}
	raw, _ := cfg.Observation("normal", 2, sample.UUID(), time.Now())
	if err = q.Intake(r.Topic, raw); !errors.Is(err, storage.ErrFull) {
		t.Fatal(err)
	}
	if q.Stats()["drops"] != uint64(1) {
		t.Fatal(q.Stats())
	}
	if err = q.Drain(context.Background(), func(context.Context, Record) error { return &PermanentError{Status: 409} }); err != nil {
		t.Fatal(err)
	}
	if q.Stats()["suspended"] != 1 || q.Stats()["pending"] != 0 {
		t.Fatal(q.Stats())
	}
	if err = q.Resume(r.ID); err != nil {
		t.Fatal(err)
	}
	if q.Stats()["pending"] != 1 {
		t.Fatal(q.Stats())
	}
}

func TestRetryAfterSchedulesWithoutBlocking(t *testing.T) {
	cfg, r := fixture(t)
	q, err := Open(cfg, t.TempDir()+"/queue", 2, 1048576)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err = q.Intake(r.Topic, r.Payload); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	q.now = func() time.Time { return clock }
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err = q.Drain(ctx, func(context.Context, Record) error { return &RetryError{After: time.Minute} }); err != nil {
		t.Fatal("retry blocked caller", err)
	}
	clock = clock.Add(59 * time.Second)
	if err = q.Drain(context.Background(), func(context.Context, Record) error { t.Fatal("Retry-After ignored"); return nil }); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	if err = q.Drain(context.Background(), func(context.Context, Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if q.Stats()["pending"] != 0 {
		t.Fatal(q.Stats())
	}
}

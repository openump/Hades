package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
)

func writeTransferTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

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
	if q.retryAt.IsZero() {
		t.Fatal("retry was not scheduled")
	}
	// Exercise the future-deadline branch without depending on random jitter or scheduler timing.
	q.retryAt = time.Now().Add(time.Hour)
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

func TestMultiAssetTransferIntake(t *testing.T) {
	secondPath := t.TempDir() + "/oven-01.metadata.json"
	secondMeta := []byte(`{
  "schema_version": "1.0",
  "message_id": "019a1234-5678-7000-8000-000000000020",
  "asset_id": "urn:openump:acme:berlin:oven-01",
  "metadata_revision": 1,
  "published_at": "2026-10-13T13:00:00.000Z",
  "display_name": "Curing Oven",
  "asset_type": "curing-oven",
  "hierarchy": {
    "enterprise": "acme",
    "site": "berlin",
    "area": "heat-treat",
    "line": "line-02",
    "asset": "oven-01"
  },
  "acquisition": {
    "gateway_id": "gateway-oven-01",
    "protocol": "simulator",
    "expected_status_interval_ms": 1000
  },
  "measurements": {
    "chamber_pressure": {
      "display_name": "Chamber pressure",
      "type": "number",
      "unit": "kPa",
      "expected_interval_ms": 1000,
      "valid_range": {
        "minimum": 80,
        "maximum": 250
      }
    }
  },
  "relationships": []
}`)
	if err := writeTransferTestFile(secondPath, secondMeta); err != nil {
		t.Fatal(err)
	}
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json,"+secondPath)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Open(cfg, t.TempDir()+"/queue", 10, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	for _, asset := range cfg.Assets() {
		obs, err := asset.Observation("normal", 1, sample.UUID(), time.Now().UTC().Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Intake(asset.Prefix+"telemetry", obs); err != nil {
			t.Fatalf("transfer intake failed for %s: %v", asset.Prefix, err)
		}
		if err := q.Intake(asset.Prefix+"metadata", asset.MetadataBytes); err != nil {
			t.Fatalf("transfer metadata intake failed for %s: %v", asset.Prefix, err)
		}
	}
	if q.Stats()["admitted"] != uint64(4) {
		t.Fatalf("expected 4 admitted transfer records across 2 assets, got %v", q.Stats())
	}
}


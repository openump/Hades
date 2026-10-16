package history

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
)

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func fixture(t *testing.T) (*sample.Config, []byte) {
	t.Helper()
	cfg, err := sample.Load("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cfg.Observation("normal", 1, sample.UUID(), time.Now().UTC().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, raw
}
func TestDurableRetryDedupAndConflict(t *testing.T) {
	cfg, raw := fixture(t)
	dir := t.TempDir() + "/intake"
	a, err := Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := a.Intake(cfg.Prefix+"telemetry", raw); err != nil || state != "accepted_synced" {
		t.Fatal(state, err)
	}
	if state, err := a.Intake(cfg.Prefix+"telemetry", raw); err != nil || state != "duplicate" {
		t.Fatal(state, err)
	}
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	obj["ingested_at"] = "2026-10-14T12:00:01.000Z"
	conflict, _ := json.Marshal(obj)
	if _, err = a.Intake(cfg.Prefix+"telemetry", conflict); err != ErrConflict {
		t.Fatal("identity conflict not rejected", err)
	}
	obj["message_id"] = sample.UUID()
	collision, _ := json.Marshal(obj)
	if _, err = a.Intake(cfg.Prefix+"telemetry", collision); err != ErrConflict {
		t.Fatal("timestamp collision not rejected", err)
	}
	a.Close()
	a, err = Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	send := func(_ context.Context, body []byte) error {
		calls++
		if calls == 1 {
			return context.DeadlineExceeded
		}
		if !strings.Contains(string(body), "hades_observation_value") || !strings.Contains(string(body), fmt.Sprint(func() int64 {
			var p map[string]any
			json.Unmarshal(raw, &p)
			stamp, _ := time.Parse(time.RFC3339Nano, p["observed_at"].(string))
			return stamp.UnixMilli()
		}())) {
			t.Fatal(string(body))
		}
		return nil
	}
	err = a.Drain(ctx, send)
	if err != nil || calls != 1 {
		t.Fatal("first attempt did not return", err, calls)
	}
	if a.retryAt.IsZero() {
		t.Fatal("retry was not scheduled")
	}
	// Exercise the future-deadline branch without depending on random jitter or scheduler timing.
	a.retryAt = time.Now().Add(time.Hour)
	if err = a.Drain(ctx, send); err != nil || calls != 1 {
		t.Fatal("retry ignored backoff", err, calls)
	}
	a.retryAt = time.Time{}
	err = a.Drain(ctx, send)
	if err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	for _, record := range a.records {
		if record.State != "api_accepted" {
			t.Fatal(record.State)
		}
	}
	a.Close()
	a, err = Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := a.Intake(cfg.Prefix+"telemetry", raw); err != nil || state != "duplicate" {
		t.Fatal("restart lost dedup", state, err)
	}
	defer a.Close()
}
func TestQualityAndNonNumericNeverFabricateValues(t *testing.T) {
	cfg, raw := fixture(t)
	var p map[string]any
	json.Unmarshal(raw, &p)
	reading := p["measurements"].(map[string]any)["motor_temperature"].(map[string]any)
	for _, value := range []any{nil, "running"} {
		reading["value"] = value
		reading["quality"] = "bad"
		reading["reason"] = "test"
		if value != nil {
			cfg.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any)["type"] = "string"
			reading["unit"] = "1"
			cfg.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any)["unit"] = "1"
		}
		body, _ := json.Marshal(p)
		obj, err := cfg.Validator.Validate("telemetry", body)
		if err != nil {
			t.Fatal(err)
		}
		lines, keys, skips, err := Map(cfg, obj)
		if err != nil || skips != 1 || len(keys) != 1 || strings.Contains(string(lines), "hades_observation_value") {
			t.Fatal(string(lines), keys, skips, err)
		}
	}
}
func TestOutOfRetentionRemainsOwned(t *testing.T) {
	cfg, raw := fixture(t)
	var p map[string]any
	json.Unmarshal(raw, &p)
	p["observed_at"] = time.Now().Add(-48 * time.Hour).UTC().Format(sample.Timestamp)
	raw, _ = json.Marshal(p)
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.Intake(cfg.Prefix+"telemetry", raw); err != nil {
		t.Fatal(err)
	}
	if err = a.Drain(context.Background(), func(context.Context, []byte) error { t.Fatal("out-of-retention sample sent to sink"); return nil }); err != nil {
		t.Fatal(err)
	}
	if a.Stats()["suspended"].(int) != 1 {
		t.Fatal(a.Stats())
	}
}
func TestMultiAssetHistoryIntakeAndMapping(t *testing.T) {
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
	if err := writeTestFile(secondPath, secondMeta); err != nil {
		t.Fatal(err)
	}
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json,"+secondPath)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	assets := cfg.Assets()
	if len(assets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(assets))
	}
	for _, asset := range assets {
		obs, err := asset.Observation("normal", 1, sample.UUID(), time.Now().UTC().Add(-time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if state, err := a.Intake(asset.Prefix+"telemetry", obs); err != nil || state != "accepted_synced" {
			t.Fatalf("intake failed for %s: %v (%s)", asset.Prefix, err, state)
		}
	}
	if a.Stats()["admitted"].(uint64) != 2 {
		t.Fatalf("expected 2 admitted records, got %v", a.Stats())
	}
}

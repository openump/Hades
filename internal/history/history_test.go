package history

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
)

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
	err = a.Drain(ctx, func(_ context.Context, body []byte) error {
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
	})
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

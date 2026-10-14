package sample

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestScenariosAndUnits(t *testing.T) {
	cfg, err := Load("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"normal", "bad-quality", "missing-reading"} {
		raw, err := cfg.Observation(scenario, 2, "019a1234-5678-7000-8000-000000000002", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		payload, err := cfg.Validator.Validate("telemetry", raw)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := cfg.Inspect(cfg.Prefix+"telemetry", raw, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		readings := payload["measurements"].(map[string]any)
		r := readings["motor_temperature"].(map[string]any)
		if scenario == "normal" && r["quality"] != "good" {
			t.Fatal(r)
		}
		if scenario != "normal" && r["quality"] != "bad" {
			t.Fatal(r)
		}
		if scenario == "missing-reading" && r["value"] != nil {
			t.Fatal("fabricated value")
		}
		if summary["type"] != "telemetry" {
			t.Fatal(summary)
		}
	}
	cfg.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any)["unit"] = "K"
	cfg.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any)["valid_range"] = map[string]any{"minimum": json.Number("233.15"), "maximum": json.Number("423.15")}
	raw, err := cfg.Observation("normal", 0, "019a1234-5678-7000-8000-000000000002", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"unit":"K"`)) || !bytes.Contains(raw, []byte(`340.35`)) {
		t.Fatal(string(raw))
	}
}
func TestRetryPreservesIdentityAndBody(t *testing.T) {
	cfg, err := Load("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cfg.Observation("normal", 0, "019a1234-5678-7000-8000-000000000002", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(raw)
	attempts := 0
	err = Send(context.Background(), raw, func(_ context.Context, payload []byte) error {
		attempts++
		if !bytes.Equal(payload, original) {
			t.Fatal("retry mutated observation")
		}
		if attempts == 1 {
			payload[0] = 'x'
			return errors.New("lost acknowledgement")
		}
		return nil
	})
	if err != nil || attempts != 2 || !bytes.Equal(raw, original) {
		t.Fatalf("attempts=%d: %v", attempts, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Send(ctx, raw, func(context.Context, []byte) error { t.Fatal("published after cancellation"); return nil }); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestReadingContextSupportsLabWithoutAllowingUnknownMeasurement(t *testing.T) {
	if _, err := LoadReadingContext("../..", "../../examples/site/modbus-lab.metadata.json"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/site/modbus-lab.metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(raw, &m)
	defs := m["measurements"].(map[string]any)
	defs["unsupported"] = defs["motor_temperature"]
	delete(defs, "motor_temperature")
	raw, _ = json.Marshal(m)
	path := t.TempDir() + "/metadata.json"
	os.WriteFile(path, raw, 0600)
	if _, err = LoadReadingContext("../..", path); err == nil {
		t.Fatal("unsupported context accepted by reading examples")
	}
}

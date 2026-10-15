package normalize

import (
	"encoding/json"
	"github.com/openump/Hades/internal/sample"
	"strings"
	"testing"
)

func TestApprovedNumericProfilePreservesMultipleReadingsAndRejectsUnknown(t *testing.T) {
	legacy, payload := fixture(t)
	defs := legacy.Config.Metadata["measurements"].(map[string]any)
	defs["pressure"] = map[string]any{"type": "number", "unit": "bar", "expected_interval_ms": json.Number("1000")}
	engine, err := NewApprovedNumeric(legacy.Config)
	if err != nil {
		t.Fatal(err)
	}
	payload["signals"] = map[string]any{
		"motor_temperature": map[string]any{"value": 67.2, "unit": "Cel", "quality": "good"},
		"pressure":          map[string]any{"value": nil, "unit": "bar", "quality": "bad", "reason": "timeout"},
	}
	raw, _ := json.Marshal(payload)
	out, err := engine.Map(raw)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	json.Unmarshal(out, &normalized)
	if len(normalized["measurements"].(map[string]any)) != 2 || normalized["message_id"] != payload["message_id"] {
		t.Fatal(normalized)
	}
	signals := payload["signals"].(map[string]any)
	signals["unauthorized"] = signals["pressure"]
	raw, _ = json.Marshal(payload)
	if _, err = engine.Map(raw); err == nil {
		t.Fatal("unapproved signal accepted")
	}
	delete(signals, "unauthorized")
	signals["pressure"].(map[string]any)["unit"] = "Pa"
	raw, _ = json.Marshal(payload)
	if _, err = engine.Map(raw); err == nil {
		t.Fatal("unit mismatch accepted")
	}
}

func TestNumericIdentityPreservesLargeReading(t *testing.T) {
	legacy, payload := fixture(t)
	delete(legacy.Config.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any), "valid_range")
	engine, err := NewApprovedNumeric(legacy.Config)
	if err != nil {
		t.Fatal(err)
	}
	payload["signals"] = map[string]any{"motor_temperature": map[string]any{"value": json.Number("9007199254740993"), "unit": "Cel", "quality": "good"}}
	raw, _ := json.Marshal(payload)
	out, err := engine.Map(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatal(string(out))
	}
}

func TestOPCUAMockPreservesSparseReadingAndProvenance(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/opcua-mock.metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewOPCUAMock(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, payload := fixture(t)
	payload["metadata_revision"] = 3
	payload["source"].(map[string]any)["protocol"] = "opc-ua"
	payload["source"].(map[string]any)["gateway_id"] = "opcua-mock-01"
	payload["signals"] = map[string]any{"cycle_counter": map[string]any{"value": 4294967295, "unit": "1", "quality": "uncertain"}}
	payload["extensions"] = map[string]any{"opcua.status_code": 1073741824, "opcua.source_timestamp": "2026-10-15T10:00:00.1234567Z"}
	raw, _ := json.Marshal(payload)
	normalized, err := engine.Map(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(normalized), "4294967295") || !strings.Contains(string(normalized), "1234567Z") {
		t.Fatal(string(normalized))
	}
	payload["signals"].(map[string]any)["unauthorized"] = payload["signals"].(map[string]any)["cycle_counter"]
	raw, _ = json.Marshal(payload)
	if _, err = engine.Map(raw); err == nil {
		t.Fatal("unapproved reading accepted")
	}
}

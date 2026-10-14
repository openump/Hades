package normalize

import (
	"encoding/json"
	"github.com/openump/Hades/internal/sample"
	"os"
	"testing"
)

func fixture(t *testing.T) (*Engine, map[string]any) {
	t.Helper()
	cfg, err := sample.Load("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := os.ReadFile("../../config/normalization/temperature.blobl")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(cfg, string(mapping))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/raw-observations/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	return engine, obj
}
func TestMappingPreservesIdentityAndMissingQuality(t *testing.T) {
	e, p := fixture(t)
	for _, missing := range []bool{false, true} {
		if missing {
			p["signals"].(map[string]any)["temperature"] = map[string]any{"value": nil, "unit": "deciCel", "quality": "bad", "reason": "timeout"}
		}
		raw, _ := json.Marshal(p)
		normalized, err := e.Map(raw)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		json.Unmarshal(normalized, &out)
		for _, key := range []string{"message_id", "asset_id", "observed_at", "ingested_at"} {
			if out[key] != p[key] {
				t.Fatalf("changed %s", key)
			}
		}
		reading := out["measurements"].(map[string]any)["motor_temperature"].(map[string]any)
		if missing {
			if reading["value"] != nil || reading["quality"] != "bad" {
				t.Fatal(reading)
			}
		} else if reading["value"] != 67.2 {
			t.Fatal(reading)
		}
	}
}
func TestRejectInvalidUnitAndUnknownSignal(t *testing.T) {
	for _, mode := range []string{"unit", "extra", "type"} {
		e, p := fixture(t)
		signals := p["signals"].(map[string]any)
		switch mode {
		case "unit":
			signals["temperature"].(map[string]any)["unit"] = "K"
		case "extra":
			signals["secret"] = signals["temperature"]
		case "type":
			signals["temperature"].(map[string]any)["value"] = "672"
		}
		raw, _ := json.Marshal(p)
		if _, err := e.Map(raw); err == nil {
			t.Fatalf("accepted %s", mode)
		}
	}
}

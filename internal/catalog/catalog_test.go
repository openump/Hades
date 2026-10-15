package catalog

import (
	"encoding/json"
	"github.com/openump/Hades/internal/sample"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCatalogExactAssetRevisionAndUnits(t *testing.T) {
	dir := t.TempDir()
	paths := []string{}
	for i, name := range []string{"press-04", "press-05"} {
		raw, err := os.ReadFile("../../examples/site/metadata.json")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(raw, &m)
		m["asset_id"] = "urn:openump:acme:berlin:" + name
		m["hierarchy"].(map[string]any)["asset"] = name
		m["acquisition"].(map[string]any)["protocol"] = []string{"modbus-tcp", "opc-ua"}[i]
		m["measurements"].(map[string]any)["pressure"] = map[string]any{"display_name": "Pressure", "type": "number", "unit": "kPa", "expected_interval_ms": 1000}
		raw, _ = json.Marshal(m)
		path := filepath.Join(dir, name+".json")
		os.WriteFile(path, raw, 0600)
		paths = append(paths, path)
	}
	c, err := Load("../..", paths)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = c.Context("urn:openump:acme:berlin:unknown", json.Number("1")); err == nil {
		t.Fatal("unknown asset")
	}
	if _, err = c.Context("urn:openump:acme:berlin:press-04", json.Number("2")); err == nil {
		t.Fatal("unknown revision")
	}
	if _, err = Load("../..", append(paths, paths[0])); err == nil {
		t.Fatal("duplicate asset accepted")
	}
	cfg, err := c.Context("urn:openump:acme:berlin:press-05", json.Number("1"))
	if err != nil || len(cfg.Metadata["measurements"].(map[string]any)) != 2 {
		t.Fatal(cfg, err)
	}
	originalProtocol := cfg.Metadata["acquisition"].(map[string]any)["protocol"]
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = "simulator"
	raw, err := cfg.Observation("normal", 1, sample.UUID(), time.Now())
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = originalProtocol
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	json.Unmarshal(raw, &payload)
	payload["source"].(map[string]any)["protocol"] = "opc-ua"
	raw, _ = json.Marshal(payload)
	if _, _, err = c.Validate("telemetry", cfg.Prefix+"telemetry", raw); err != nil {
		t.Fatal(err)
	}
	payload["measurements"].(map[string]any)["motor_temperature"].(map[string]any)["unit"] = "bar"
	raw, _ = json.Marshal(payload)
	if _, _, err = c.Validate("telemetry", cfg.Prefix+"telemetry", raw); err == nil {
		t.Fatal("wrong unit accepted")
	}
	if _, _, err = c.Validate("telemetry", "openump/v1/acme/berlin/assembly/line-01/unapproved/telemetry", raw); err == nil {
		t.Fatal("unknown topic accepted")
	}

}

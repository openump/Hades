package contracts

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestPayloadFixtures(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"telemetry", "context"} {
		raw, err := os.ReadFile("../../examples/contracts/" + group + "/cases.json")
		if err != nil {
			t.Fatal(err)
		}
		var cases []struct {
			Name    string
			Kind    string
			Valid   bool
			Payload json.RawMessage
		}
		if err = json.Unmarshal(raw, &cases); err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			t.Run(c.Name, func(t *testing.T) {
				kind := c.Kind
				if kind == "" {
					kind = "telemetry"
				}
				_, err := v.Validate(kind, c.Payload)
				if (err == nil) != c.Valid {
					t.Fatalf("valid=%v: %v", c.Valid, err)
				}
			})
		}
	}
}

func TestWireBoundaries(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("../../examples/contracts/telemetry/cases.json")
	var cases []struct{ Payload json.RawMessage }
	json.Unmarshal(raw, &cases)
	raw = cases[0].Payload
	for _, token := range []string{"NaN", "Infinity", "1e999"} {
		bad := bytes.Replace(raw, []byte("67.2"), []byte(token), 1)
		if _, err := v.Validate("telemetry", bad); err == nil {
			t.Fatal("accepted", token)
		}
	}
	duplicate := append([]byte(`{"schema_version":"1.0",`), raw[1:]...)
	for _, bad := range [][]byte{duplicate, {0xff}, []byte(`{} {}`), []byte(`{"x":"\ud800"}`)} {
		if _, err := v.Validate("telemetry", bad); err == nil {
			t.Fatal("accepted malformed JSON")
		}
	}
	if _, err := v.Validate("telemetry", append(bytes.Clone(raw), bytes.Repeat([]byte(" "), 65536-len(raw))...)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate("telemetry", append(bytes.Clone(raw), bytes.Repeat([]byte(" "), 65537-len(raw))...)); err == nil {
		t.Fatal("accepted oversized payload")
	}
	if _, err := v.Validate("../secrets", raw); err == nil {
		t.Fatal("accepted unknown kind")
	}
}

func TestTopicFixtures(t *testing.T) {
	raw, _ := os.ReadFile("../../examples/contracts/assets.json")
	var assets []Asset
	if err := json.Unmarshal(raw, &assets); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(assets)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile("../../examples/contracts/topics.json")
	var cases []struct {
		Name    string
		Topic   string
		AssetID string `json:"asset_id"`
		Valid   bool
	}
	json.Unmarshal(raw, &cases)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			err := registry.ValidateTopic(c.Topic, c.AssetID)
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v: %v", c.Valid, err)
			}
		})
	}
	if _, err := NewRegistry(append(assets, assets[0])); err == nil {
		t.Fatal("accepted duplicate identity")
	}
	assets[1].Location = assets[0].Location
	if _, err := NewRegistry(assets); err == nil {
		t.Fatal("accepted duplicate path")
	}
}

func TestContextAndFreshness(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	load := func(path, kind string) map[string]any {
		raw, _ := os.ReadFile(path)
		var cases []struct{ Payload json.RawMessage }
		json.Unmarshal(raw, &cases)
		p, e := v.Validate(kind, cases[0].Payload)
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	telemetry := load("../../examples/contracts/telemetry/cases.json", "telemetry")
	metadata := load("../../examples/contracts/context/cases.json", "metadata")
	if err := ValidateContext(telemetry, metadata); err != nil {
		t.Fatal(err)
	}
	telemetry["metadata_revision"] = json.Number("2")
	if err := ValidateContext(telemetry, metadata); err == nil {
		t.Fatal("accepted wrong revision")
	}
	telemetry["metadata_revision"] = json.Number("1")
	measurements := telemetry["measurements"].(map[string]any)
	measurements["motor_temperature"].(map[string]any)["value"] = true
	if err := ValidateContext(telemetry, metadata); err == nil {
		t.Fatal("accepted bool as number")
	}
	now := time.Date(2026, 10, 13, 14, 0, 30, 0, time.UTC)
	u := 5.0
	for _, c := range []struct {
		delta time.Duration
		u     *float64
		want  string
	}{{-time.Second, &u, "fresh"}, {-30 * time.Second, &u, "stale"}, {time.Second, &u, "unknown"}, {-time.Second, nil, "unknown"}} {
		if got := Freshness(now.Add(c.delta), now, 1000, c.u); got != c.want {
			t.Fatalf("%s != %s", got, c.want)
		}
	}
}

func TestSchemaFailuresAndOfflineLoading(t *testing.T) {
	for _, remote := range []bool{false, true} {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS("../../schemas")); err != nil {
			t.Fatal(err)
		}
		path := dir + "/v1/telemetry.schema.json"
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if remote {
			raw = bytes.ReplaceAll(raw, []byte("urn:openump:schema:1.0:common"), []byte("https://unregistered.invalid/schema"))
		} else {
			raw = bytes.Replace(raw, []byte(`"type": "object"`), []byte(`"type": "invalid"`), 1)
		}
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := New(os.DirFS(dir)); err == nil {
			t.Fatal("accepted invalid or unregistered schema")
		}
	}
}

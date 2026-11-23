package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDescribe(t *testing.T) {
	got, err := describe(map[string]any{"type": "status", "producer_state": "online", "acquisition_state": "healthy"})
	if err != nil || got != "producer=online acquisition=healthy" {
		t.Fatalf("status: %q %v", got, err)
	}
	got, err = describe(map[string]any{"type": "telemetry", "measurements": map[string]any{
		"b": map[string]any{"value": json.Number("1e-7"), "unit": "1", "quality": "good", "freshness": "stale"},
		"a": map[string]any{"value": "\x1b[2J\u202eevil", "unit": "1", "quality": "uncertain", "freshness": "fresh"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Sorted, freshness kept, the JSON literal kept, hostile text escaped.
	if !strings.HasPrefix(got, `a="\x1b[2J\u202eevil" 1 (uncertain, fresh) b=1e-7 1 (good, stale)`) {
		t.Fatalf("telemetry: %q", got)
	}
	if strings.ContainsAny(got, "\x1b\u202e") {
		t.Fatal("raw control or bidi character in the output")
	}
	for _, bad := range []map[string]any{
		{"type": "status", "producer_state": "online"},
		{"type": "telemetry"},
		{"type": "telemetry", "measurements": map[string]any{"a": map[string]any{"value": 1}}},
		{"type": "command"},
	} {
		if _, err := describe(bad); err == nil {
			t.Errorf("incomplete summary accepted: %v", bad)
		}
	}
}

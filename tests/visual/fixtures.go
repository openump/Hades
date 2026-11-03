package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/openump/Hades/internal/inspect"
	"github.com/openump/Hades/internal/tui"
)

// clock is the fixed instant every fixture renders at, so captures are
// reproducible: the console takes all times from the snapshot, and hades top
// takes "now" as a parameter.
var clock = time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)

const (
	press04 = "urn:openump:acme:berlin:press-04"
	press05 = "urn:openump:acme:berlin:press-05"
	topic04 = "openump/v1/acme/berlin/assembly/line-01/press-04"
	topic05 = "openump/v1/acme/berlin/assembly/line-01/press-05"
)

func stamp(d time.Duration) string { return clock.Add(d).Format("2006-01-02T15:04:05.000Z") }

func reading(value any, unit, quality string) map[string]any {
	return map[string]any{"value": value, "unit": unit, "quality": quality}
}

func gates(results ...string) json.RawMessage {
	var g []map[string]any
	for i, r := range results {
		g = append(g, map[string]any{"gate": i + 1, "result": r})
	}
	raw, _ := json.Marshal(g)
	return raw
}

// normalModel exercises every visual state: all four quality badges, a
// producer status, receipts that executed, were rejected at a gate and await
// confirmation, quarantine counts, certificates in each expiry band, and
// services that are verified, mismatched and unreachable.
func normalModel() *inspect.Model {
	m := inspect.NewModel(func() time.Time { return clock })
	second := time.Second
	m.Declare(press04, topic04, map[string]time.Duration{"motor_temperature": second, "spindle_speed": second, "hydraulic_pressure": second, "coolant_flow": second})
	m.Declare(press05, topic05, map[string]time.Duration{"motor_temperature": second})
	m.Telemetry(map[string]any{"asset_id": press04, "observed_at": stamp(-time.Minute), "measurements": map[string]any{
		"coolant_flow": reading(12.5, "L/min", "good"), // observed a minute ago: rendered STALE
	}})
	m.Telemetry(map[string]any{"asset_id": press04, "observed_at": stamp(-time.Second), "measurements": map[string]any{
		"motor_temperature":  reading(71.25, "Cel", "good"),
		"spindle_speed":      reading(1200.0, "1/min", "uncertain"),
		"hydraulic_pressure": reading(nil, "bar", "bad"),
	}})
	m.Telemetry(map[string]any{"asset_id": press05, "observed_at": stamp(-2 * time.Second), "measurements": map[string]any{
		"motor_temperature": reading(64.0, "Cel", "good"),
	}})
	m.Status(press04, []byte(`{"producer_state":"online","acquisition_state":"healthy"}`))
	m.Status(press05, []byte(`{"producer_state":"online","acquisition_state":"degraded","reason":"poll timeout"}`))
	m.Receipt(inspect.Receipt{CommandID: "019a1234-5678-7000-8000-000000000441", Asset: press04, Parameter: "pressure_setpoint", State: "EXECUTED", Terminal: true,
		Gates: gates("PASS", "PASS", "PASS", "PASS", "PASS"), RecordedAt: clock.Add(-10 * time.Minute)})
	m.Receipt(inspect.Receipt{CommandID: "019a1234-5678-7000-8000-000000000442", Asset: press04, Parameter: "pressure_setpoint", State: "REJECTED_SAFETY_BOUNDS", Terminal: true,
		Gates: gates("PASS", "PASS", "FAIL", "NOT_EVALUATED", "NOT_EVALUATED"), RecordedAt: clock.Add(-5 * time.Minute)})
	m.Receipt(inspect.Receipt{CommandID: "019a1234-5678-7000-8000-000000000443", Asset: press04, Parameter: "pressure_setpoint", State: "AWAITING_OPERATOR_CONFIRMATION", Terminal: false,
		Gates: gates("PASS", "PASS", "PASS", "PASS", "NOT_EVALUATED"), RecordedAt: clock.Add(-time.Minute)})
	m.SetQuarantine(map[string]any{"quarantine_records": 3.0, "quarantine_bytes": 2048.0, "quarantine_evictions": 0.0, "rejected": 5.0, "storage_failures": 0.0, "disk_reserve_ok": true})
	m.SetCertificates([]inspect.Certificate{
		{Path: "/etc/hades/pki/ca.crt", Subject: "acme-berlin-site-ca", NotAfter: clock.AddDate(0, 0, 300), DaysLeft: 300},
		{Path: "/etc/hades/pki/gateway-press-04.crt", Subject: "gateway-press-04", NotAfter: clock.AddDate(0, 0, 12), DaysLeft: 12},
		{Path: "/etc/hades/pki/legacy-ingress.crt", Subject: "legacy-ingress", NotAfter: clock.AddDate(0, 0, -3), DaysLeft: -3},
	})
	m.SetServices(map[string]any{
		"/etc/hades/services/normalizer.json": map[string]any{"revision": "site-r7", "ready": true, "digest_verified": true, "child_pid": float64(4242),
			"started_at": clock.Add(-90 * time.Minute).Format(time.RFC3339), "last_output_at": clock.Add(-2 * time.Second).Format(time.RFC3339)},
		"/etc/hades/services/transfer.json": map[string]any{"revision": "site-r7", "ready": false, "digest_verified": false, "error": "live revision mismatch"},
		"/etc/hades/services/history.json":  map[string]any{"revision": "site-r7", "ready": false, "error": "dial unix /var/lib/hades/history/live.sock: connect: no such file or directory"},
	})
	return m
}

// hostile is text a plant or a compromised peer could send: terminal and
// clipboard escape sequences, newlines, bidirectional overrides and text far
// longer than any column.
var hostile = "\x1b]52;c;cHduZWQ=\x07\x1b[2J‮payload‬\nsecond line " + strings.Repeat("A-very-long-unbroken-token-", 12)

// hostileModel puts hostile text in every field the views display.
func hostileModel() *inspect.Model {
	m := inspect.NewModel(func() time.Time { return clock })
	m.Declare(press04, topic04, map[string]time.Duration{"motor_temperature": time.Second})
	m.Telemetry(map[string]any{"asset_id": press04, "observed_at": stamp(-time.Second), "measurements": map[string]any{
		"motor_temperature": reading(hostile, hostile, "good"),
	}})
	m.Status(press04, []byte(`{"producer_state":"online","acquisition_state":"degraded","reason":`+mustJSON(hostile)+`}`))
	m.Receipt(inspect.Receipt{CommandID: hostile, Asset: press04, Parameter: "pressure_setpoint", State: hostile, Gates: gates("PASS", "FAIL"), RecordedAt: clock})
	m.SetQuarantine(map[string]any{"error": hostile})
	m.SetCertificates([]inspect.Certificate{{Path: "/etc/hades/pki/" + hostile, Subject: hostile, NotAfter: clock.AddDate(0, 0, 5), DaysLeft: 5}})
	m.SetServices(map[string]any{"/etc/hades/services/" + strings.Repeat("x", 120) + ".json": map[string]any{"revision": hostile, "ready": false, "error": hostile}})
	return m
}

func mustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

// topState is hades top's state for a model's snapshot, with fixed process
// resources so the supervisor pane shows RSS and CPU deterministically.
func topState(m *inspect.Model) *tui.State {
	snap := m.Snapshot()
	s := &tui.State{}
	s.Apply(tui.Sample{Snapshot: &snap, At: clock, Resources: map[int]tui.Resource{4242: {RSSBytes: 38 << 20, CPUPercent: 3.2, CPUKnown: true}}})
	return s
}

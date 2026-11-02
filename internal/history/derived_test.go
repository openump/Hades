package history

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/infer"
)

const wear = "estimated_bearing_wear"

var press = "urn:openump:acme:berlin:press-04"

func declarations() contracts.DerivedDeclarations {
	d := contracts.DerivedDeclarations{}
	d.Add(contracts.SoftSensor{ModelID: "press-04-thermal", ModelVersion: "1.0.0", Asset: press, Measurement: wear, Unit: "%"})
	return d
}

// derived builds a derived publication with hades-infer's own builder, so the
// historian is tested against what the producer actually sends.
func derived(t *testing.T, measurement, unit, version string, end time.Time) (string, []byte) {
	t.Helper()
	out := &infer.Output{ModelID: "press-04-thermal", ModelVersion: version, WindowStart: end.Add(-time.Minute), WindowEnd: end, Result: infer.Result{Confidence: 0.8}}
	pubs, err := infer.DerivedPublications([]infer.SoftSensorObservation{{Asset: press, Measurement: measurement, Value: 12.5, Unit: unit}}, out,
		func(string) (string, error) { return "openump/v1/acme/berlin/assembly/line-01/press-04/", nil })
	if err != nil {
		t.Fatal(err)
	}
	return pubs[0].Topic, pubs[0].Payload
}

func TestDerivedValueIsStoredInItsOwnSeries(t *testing.T) {
	cfg, telemetry := fixture(t)
	a, err := OpenWith(cfg, t.TempDir()+"/intake", declarations())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	end := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	topic, raw := derived(t, wear, "%", "1.0.0", end)
	if state, err := a.Intake(topic, raw); err != nil || state != "accepted_synced" {
		t.Fatal(state, err)
	}
	if _, err := a.Intake(cfg.Prefix+"telemetry", telemetry); err != nil {
		t.Fatal(err)
	}
	if state, _ := a.Intake(topic, raw); state != "duplicate" {
		t.Fatal("a redelivered derived value must deduplicate, got", state)
	}
	var lines []string
	if err := a.Drain(context.Background(), func(_ context.Context, body []byte) error {
		lines = append(lines, strings.Split(strings.TrimSpace(string(body)), "\n")...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var derivedLines, deviceLines []string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "hades_derived_"):
			derivedLines = append(derivedLines, l)
		case strings.HasPrefix(l, "hades_observation_"):
			deviceLines = append(deviceLines, l)
		default:
			t.Fatal("unexpected line", l)
		}
	}
	for _, l := range derivedLines {
		if strings.Contains(l, "window_start_ms") {
			t.Fatal("a per-window label would create a new series every window:", l)
		}
	}
	if len(derivedLines) != 2 {
		t.Fatalf("derived value and confidence expected: %v", derivedLines)
	}
	value := derivedLines[0]
	for _, want := range []string{`hades_derived_value{`, `asset_id="` + press + `"`, `measurement="` + wear + `"`, `unit="%"`, `model_id="press-04-thermal"`, `model_version="1.0.0"`, " 12.5 ", " " + jsonInt(end.UnixMilli())} {
		if !strings.Contains(value, want) {
			t.Fatalf("derived line lacks %q: %s", want, value)
		}
	}
	for _, l := range deviceLines {
		if strings.Contains(l, wear) || strings.Contains(l, "model_id") {
			t.Fatal("a derived value leaked into a device series:", l)
		}
	}
	if a.Stats()["derived_admitted"].(uint64) != 1 {
		t.Fatal(a.Stats())
	}
}

func TestDerivedValueClaimingDeviceTelemetryIsRejected(t *testing.T) {
	cfg, telemetry := fixture(t)
	a, err := OpenWith(cfg, t.TempDir()+"/intake", declarations())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	end := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)

	topic, raw := derived(t, "motor_temperature", "Cel", "1.0.0", end) // a device measurement name
	if _, err := a.Intake(topic, raw); err == nil || !strings.Contains(err.Error(), "device measurement") {
		t.Fatal("a derived value named like a device measurement must be refused:", err)
	}
	_, raw = derived(t, wear, "%", "1.0.0", end)
	if _, err := a.Intake(cfg.Prefix+"telemetry", raw); err == nil {
		t.Fatal("a derived payload on the device telemetry topic must be refused")
	}
	if _, err := a.Intake(strings.Replace(topic, "/derived/motor_temperature", "/derived/"+wear, 1), telemetry); err == nil {
		t.Fatal("a device telemetry payload on a derived topic must be refused")
	}
	for name, mutate := range map[string]func() (string, []byte){
		"undeclared measurement": func() (string, []byte) { return derived(t, "undeclared_estimate", "%", "1.0.0", end) },
		"unit differs":           func() (string, []byte) { return derived(t, wear, "K", "1.0.0", end) },
		"unverified model":       func() (string, []byte) { return derived(t, wear, "%", "2.0.0", end) },
		"wrong derived topic": func() (string, []byte) {
			tp, r := derived(t, wear, "%", "1.0.0", end)
			return strings.Replace(tp, "/derived/"+wear, "/derived/other", 1), r
		},
	} {
		tp, r := mutate()
		if _, err := a.Intake(tp, r); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	if a.Stats()["admitted"].(uint64) != 0 {
		t.Fatal("nothing may be admitted", a.Stats())
	}
}

func TestDerivedIngestionIsOptIn(t *testing.T) {
	cfg, _ := fixture(t)
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	topic, raw := derived(t, wear, "%", "1.0.0", time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond))
	if _, err := a.Intake(topic, raw); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatal("without -derived-bundles a derived value must be refused:", err)
	}
}

func TestDerivedRecordSurvivesRestartAndConflictsAreDetected(t *testing.T) {
	cfg, _ := fixture(t)
	dir := t.TempDir() + "/intake"
	a, err := OpenWith(cfg, dir, declarations())
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	topic, raw := derived(t, wear, "%", "1.0.0", end)
	if _, err := a.Intake(topic, raw); err != nil {
		t.Fatal(err)
	}
	// hades-infer re-sending the same window (after a restart) deduplicates.
	if _, again := derived(t, wear, "%", "1.0.0", end); string(again) != string(raw) {
		t.Fatal("a re-sent window must carry the same identity")
	} else if state, err := a.Intake(topic, again); err != nil || state != "duplicate" {
		t.Fatal("a re-sent window must be a duplicate, not a conflict:", state, err)
	}
	// A different value claiming the same model window is a real conflict.
	var p map[string]any
	json.Unmarshal(raw, &p)
	p["value"] = 99.0
	p["observation_id"] = "019a1234-5678-7000-8000-000000000999"
	other, _ := json.Marshal(p)
	if _, err := a.Intake(topic, other); err != ErrConflict {
		t.Fatal("a different value for the same model window must conflict:", err)
	}
	a.Close()
	// Retiring the model (no declarations) or turning derived ingestion off must
	// not stop the historian: owned records are still delivered.
	for _, decl := range []contracts.DerivedDeclarations{nil, {}} {
		b, err := OpenWith(cfg, dir, decl)
		if err != nil {
			t.Fatal("owned derived records must recover after the model is retired:", err)
		}
		if b.Stats()["pending"].(int) != 1 {
			t.Fatal(b.Stats())
		}
		b.Close()
	}
}

func jsonInt(v int64) string { raw, _ := json.Marshal(v); return string(raw) }

func TestDerivedSeriesIsStableAcrossWindows(t *testing.T) {
	cfg, _ := fixture(t)
	a, err := OpenWith(cfg, t.TempDir()+"/intake", declarations())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	end := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	for i := 0; i < 3; i++ {
		topic, raw := derived(t, wear, "%", "1.0.0", end.Add(time.Duration(i)*time.Second))
		if _, err := a.Intake(topic, raw); err != nil {
			t.Fatal(err)
		}
	}
	series := map[string]bool{}
	a.Drain(context.Background(), func(_ context.Context, body []byte) error {
		for _, l := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			if strings.HasPrefix(l, "hades_derived_value{") {
				series[l[:strings.Index(l, "}")+1]] = true
			}
		}
		return nil
	})
	if len(series) != 1 {
		t.Fatalf("three windows of one soft sensor must share one series: %v", series)
	}
}

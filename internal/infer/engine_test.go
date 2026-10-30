package infer

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

const press = "urn:openump:acme:berlin:press-04"

// testModel builds a verified-shaped model around runner with tight budgets.
func testModel(version string, rollback *string, latencyMS, failures int, runner Runner) *Model {
	m := Manifest{ModelID: "press-04-thermal", ModelVersion: version, RuntimeType: "statistical_v1"}
	m.Inputs.Bindings = append(m.Inputs.Bindings, struct {
		Asset       string `json:"asset"`
		Measurement string `json:"measurement"`
		Unit        string `json:"unit"`
	}{press, "motor_temperature", "Cel"})
	m.Inputs.WindowSizeSamples = 30
	m.Inputs.WindowDurationMS = 60_000
	m.Inputs.MinGoodRatio = 0.9
	m.Budgets.MaxInferenceLatencyMS = latencyMS
	m.Budgets.MaxConsecutiveFailures = failures
	m.Budgets.RollbackToVersion = rollback
	return &Model{Bundle: &Bundle{Manifest: m}, Runner: runner}
}

func stat(t *testing.T, artifact string) Runner {
	t.Helper()
	model, err := ParseStatisticalModel([]byte(artifact))
	if err != nil {
		t.Fatal(err)
	}
	return &statisticalRunner{model: model}
}

func feed(e *Engine, base time.Time, values []float64) {
	for i, v := range values {
		e.Observe(press, "motor_temperature", Sample{Value: v, At: base.Add(time.Duration(i) * time.Second), Good: true})
	}
}

func TestDriftStreamRaisesDeterministicAnomalyWithProvenance(t *testing.T) {
	artifact := `{"detectors":[{"kind":"ewma_drift","measurement":"motor_temperature","alpha":0.2,"scale":1}],
		"soft_sensors":[{"kind":"ewma","measurement":"motor_temperature","output":"motor_temperature_trend","alpha":0.2}]}`
	base := time.Unix(1_800_000_000, 0)
	drift := make([]float64, 30)
	for i := range drift {
		drift[i] = 70 + 0.25*float64(i)
	}
	var outputs []*Output
	for range 2 {
		e, err := NewEngine(testModel("1.0.0", nil, 500, 3, stat(t, artifact)), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		feed(e, base, drift)
		out, err := e.Step(context.Background())
		if err != nil || out == nil {
			t.Fatal(out, err)
		}
		outputs = append(outputs, out)
	}
	a, b := outputs[0], outputs[1]
	if a.Result.Score < 0.7 || a.Result.Score != b.Result.Score {
		t.Fatalf("drift must score high and deterministically: %.3f vs %.3f", a.Result.Score, b.Result.Score)
	}
	if a.ModelID != "press-04-thermal" || a.ModelVersion != "1.0.0" {
		t.Fatal("provenance must name the producing model", a.ModelID, a.ModelVersion)
	}
	if !a.WindowStart.Equal(base) || !a.WindowEnd.Equal(base.Add(29*time.Second)) {
		t.Fatal(a.WindowStart, a.WindowEnd)
	}
	if trend := a.Result.SoftSensors["motor_temperature_trend"]; !(trend > 72 && trend < 77.5) {
		t.Fatalf("soft-sensor trend %.2f", trend)
	}
}

func TestEngineWaitsForFullGoodWindow(t *testing.T) {
	e, _ := NewEngine(testModel("1.0.0", nil, 500, 3, stat(t, `{"detectors":[{"kind":"zscore","measurement":"motor_temperature","scale":3}]}`)), nil, nil)
	feed(e, time.Unix(1_800_000_000, 0), make([]float64, 10))
	if out, err := e.Step(context.Background()); out != nil || err != nil {
		t.Fatal("a partial window must not run", out, err)
	}
}

type slowRunner struct{ delay time.Duration }

func (r *slowRunner) Infer(ctx context.Context, _ Input) (Result, error) {
	time.Sleep(r.delay) // deliberately ignores ctx
	return Result{Score: 0.1}, nil
}
func (r *slowRunner) Close() error { return nil }

type failingRunner struct{ closed bool }

func (r *failingRunner) Infer(context.Context, Input) (Result, error) {
	return Result{}, errors.New("model crashed")
}
func (r *failingRunner) Close() error { r.closed = true; return nil }

type recorder struct {
	mu     sync.Mutex
	events []StatusEvent
}

func (r *recorder) add(e StatusEvent) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }

func TestSlowModelRollsBackToKnownGoodThenStandby(t *testing.T) {
	previous := "1.0.0"
	good := testModel("1.0.0", nil, 500, 2, stat(t, `{"detectors":[{"kind":"zscore","measurement":"motor_temperature","scale":3}]}`))
	slow := testModel("1.1.0", &previous, 50, 2, &slowRunner{delay: 400 * time.Millisecond})
	events := &recorder{}
	e, err := NewEngine(slow, good, events.add)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_800_000_000, 0)
	window := make([]float64, 30)
	for i := range window {
		window[i] = 70 + 0.1*math.Sin(float64(i))
	}
	feed(e, base, window)
	started := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := e.Step(context.Background()); err == nil || !strings.Contains(err.Error(), "latency budget") {
			t.Fatalf("step %d: expected a latency breach, got %v", i, err)
		}
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("latency budget not enforced on a runner ignoring ctx: %v", elapsed)
	}
	if e.Active() != "press-04-thermal@1.0.0" {
		t.Fatal("expected rollback to the known-good version, active:", e.Active())
	}
	if len(events.events) != 1 || events.events[0].State != StateDegraded || !strings.Contains(events.events[0].Reason, "latency-budget") {
		t.Fatal(events.events)
	}
	// The known-good model runs on a fresh window after the swap.
	feed(e, base.Add(time.Hour), window)
	if out, err := e.Step(context.Background()); err != nil || out == nil || out.ModelVersion != "1.0.0" {
		t.Fatal(out, err)
	}
}

func TestFailingModelWithoutRollbackEntersStandby(t *testing.T) {
	failing := &failingRunner{}
	events := &recorder{}
	e, _ := NewEngine(testModel("1.0.0", nil, 500, 3, failing), nil, events.add)
	feed(e, time.Unix(1_800_000_000, 0), make([]float64, 30))
	for i := 0; i < 3; i++ {
		if _, err := e.Step(context.Background()); err == nil {
			t.Fatal("expected failure")
		}
	}
	if e.Active() != "standby" || !failing.closed {
		t.Fatal("expected standby with the failed runner closed:", e.Active(), failing.closed)
	}
	if len(events.events) != 1 || !strings.Contains(events.events[0].Reason, "failure-budget") || events.events[0].Model != "standby" {
		t.Fatal(events.events)
	}
	if out, err := e.Step(context.Background()); out != nil || err != nil {
		t.Fatal("standby must run nothing", out, err)
	}
	if e.Observe(press, "motor_temperature", Sample{Value: 1, At: time.Now(), Good: true}) {
		t.Fatal("standby holds no windows")
	}
}

func TestFallbackMustBeTheDeclaredRollback(t *testing.T) {
	v1 := "1.0.0"
	if _, err := NewEngine(testModel("1.2.0", &v1, 500, 3, &failingRunner{}), testModel("0.9.0", nil, 500, 3, &failingRunner{}), nil); err == nil {
		t.Fatal("a fallback other than rollback_to_version must be refused")
	}
}

func telemetry(at time.Time, value any, unit, quality string) []byte {
	m := map[string]any{"value": value, "unit": unit, "quality": quality}
	if value == nil {
		m["reason"] = "sensor-fault"
	}
	raw, _ := json.Marshal(map[string]any{
		"schema_version": "1.0", "message_id": "019a1234-5678-4000-8000-000000000040",
		"asset_id": press, "metadata_revision": 1,
		"observed_at": at.UTC().Format("2006-01-02T15:04:05.000Z"), "ingested_at": at.UTC().Format("2006-01-02T15:04:05.000Z"),
		"source":       map[string]any{"gateway_id": "gw", "protocol": "opc-ua", "boot_id": "019a1234-5678-4000-8000-000000000041", "sequence": 1, "clock_origin": "device", "clock_uncertainty_ms": nil},
		"measurements": map[string]any{"motor_temperature": m, "unbound_signal": map[string]any{"value": 1, "unit": "1", "quality": "good"}},
	})
	return raw
}

const pressTelemetry = "openump/v1/acme/berlin/assembly/line-01/press-04/telemetry"

func TestObserveTelemetryHonorsContractQualityAndUnit(t *testing.T) {
	v := testValidator(t)
	reg := registry(t)
	e, _ := NewEngine(testModel("1.0.0", nil, 500, 3, stat(t, `{"detectors":[{"kind":"zscore","measurement":"motor_temperature","scale":3}]}`)), nil, nil)
	base := time.Unix(1_800_000_000, 0)
	cases := []struct {
		raw  []byte
		good bool
	}{
		{telemetry(base, 70.5, "Cel", "good"), true},
		{telemetry(base.Add(1*time.Second), 70.5, "K", "good"), false},        // unit mismatch
		{telemetry(base.Add(2*time.Second), 70.5, "Cel", "uncertain"), false}, // not good
		{telemetry(base.Add(3*time.Second), nil, "Cel", "bad"), false},        // null value
	}
	for i, c := range cases {
		n, err := e.ObserveTelemetry(v, reg, pressTelemetry, c.raw)
		if err != nil || n != 1 {
			t.Fatalf("case %d: accepted %d, err %v", i, n, err)
		}
	}
	w := e.windows[windowKey(press, "motor_temperature")]
	if got := w.GoodValues(nil); len(got) != 1 || got[0] != 70.5 {
		t.Fatal("only the good, correctly-united sample may feed the model", got)
	}
	if _, err := e.ObserveTelemetry(v, reg, pressTelemetry, []byte(`{"asset_id":"x"}`)); err == nil {
		t.Fatal("non-contract telemetry must be rejected")
	}
}

func TestObserveTelemetryRejectsAssetNotMatchingTopic(t *testing.T) {
	e, _ := NewEngine(testModel("1.0.0", nil, 500, 3, stat(t, `{"detectors":[{"kind":"zscore","measurement":"motor_temperature","scale":3}]}`)), nil, nil)
	other := "openump/v1/acme/berlin/assembly/line-01/press-05/telemetry"
	if _, err := e.ObserveTelemetry(testValidator(t), registry(t), other, telemetry(time.Unix(1_800_000_000, 0), 70.0, "Cel", "good")); err == nil {
		t.Fatal("a press-04 payload on another asset's topic must not feed press-04's model")
	}
}

func TestStalledSourceIsNotReplayed(t *testing.T) {
	e, _ := NewEngine(testModel("1.0.0", nil, 500, 3, stat(t, `{"detectors":[{"kind":"zscore","measurement":"motor_temperature","scale":3}]}`)), nil, nil)
	base := time.Unix(1_800_000_000, 0)
	feed(e, base, make([]float64, 30))
	if out, err := e.Step(context.Background()); out == nil || err != nil {
		t.Fatal("first full window must run", out, err)
	}
	for i := 0; i < 3; i++ {
		if out, err := e.Step(context.Background()); out != nil || err != nil {
			t.Fatal("a stalled source must not republish the same window", out, err)
		}
	}
	// One fresh sample, still inside the 60 s window span, allows the next run.
	e.Observe(press, "motor_temperature", Sample{Value: 0, At: base.Add(30 * time.Second), Good: true})
	if out, _ := e.Step(context.Background()); out == nil {
		t.Fatal("a new sample must allow the next run")
	}
}

type countingSlowRunner struct {
	mu      sync.Mutex
	active  int
	maxSeen int
	delay   time.Duration
}

func (r *countingSlowRunner) Infer(context.Context, Input) (Result, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.maxSeen {
		r.maxSeen = r.active
	}
	r.mu.Unlock()
	time.Sleep(r.delay) // ignores ctx
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return Result{Score: 0.1, Contributions: map[string]float64{"motor_temperature": 0.1}}, nil
}
func (r *countingSlowRunner) Close() error { return nil }

// TestOverrunningRunnerIsNeverCalledConcurrently: while an abandoned call is
// still running, new steps fail fast instead of starting more goroutines.
func TestOverrunningRunnerIsNeverCalledConcurrently(t *testing.T) {
	runner := &countingSlowRunner{delay: 300 * time.Millisecond}
	e, _ := NewEngine(testModel("1.0.0", nil, 20, 100, runner), nil, nil)
	base := time.Unix(1_800_000_000, 0)
	for i := 0; i < 8; i++ {
		feed(e, base.Add(time.Duration(i)*time.Minute), make([]float64, 30))
		e.Step(context.Background())
	}
	time.Sleep(400 * time.Millisecond)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.maxSeen != 1 {
		t.Fatalf("runner was called %d times concurrently; abandoned calls must not overlap", runner.maxSeen)
	}
}

// TestOneStalledStreamBlocksRescoring: a fresh sample on one input does not
// re-score a frozen window on another.
func TestOneStalledStreamBlocksRescoring(t *testing.T) {
	m := testModel("1.0.0", nil, 500, 3, stat(t, `{"detectors":[{"kind":"zscore","measurement":"motor_temperature","scale":3},{"kind":"zscore","measurement":"vibration","scale":3}]}`))
	m.Bundle.Manifest.Inputs.Bindings = append(m.Bundle.Manifest.Inputs.Bindings, struct {
		Asset       string `json:"asset"`
		Measurement string `json:"measurement"`
		Unit        string `json:"unit"`
	}{press, "vibration", "1"})
	e, _ := NewEngine(m, nil, nil)
	base := time.Unix(1_800_000_000, 0)
	for i := 0; i < 30; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		e.Observe(press, "motor_temperature", Sample{Value: 70, At: at, Good: true})
		e.Observe(press, "vibration", Sample{Value: float64(i % 3), At: at, Good: true})
	}
	if out, err := e.Step(context.Background()); out == nil || err != nil {
		t.Fatal("first run", out, err)
	}
	// Temperature keeps arriving; vibration has stalled.
	e.Observe(press, "motor_temperature", Sample{Value: 70, At: base.Add(30 * time.Second), Good: true})
	if out, _ := e.Step(context.Background()); out != nil {
		t.Fatal("a stalled vibration stream must not be re-scored as a new window")
	}
}

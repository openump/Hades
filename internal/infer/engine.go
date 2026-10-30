package infer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is the engine's producer_status-compatible health.
type State string

const (
	StateGood     State = "good"
	StateDegraded State = "degraded"
)

// Model is a verified bundle with its runner.
type Model struct {
	Bundle *Bundle
	Runner Runner
	busy   chan struct{} // non-nil while this model's runner call is outstanding
}

// StatusEvent reports an engine health transition.
type StatusEvent struct {
	State  State
	Reason string
	Model  string // model_id@model_version now active, or "standby"
	At     time.Time
}

// Output is one window's published result, tagged with the producing model.
// Manifest is that model's manifest, so publications use the producing
// model's output bindings even after a rollback.
type Output struct {
	Manifest     Manifest
	ModelID      string
	ModelVersion string
	Result       Result
	WindowStart  time.Time
	WindowEnd    time.Time
}

// Engine runs the active model over its bound windows and enforces the
// manifest budgets. When a model breaches its latency or failure budget it
// atomically falls back to the rollback model, or to standby (no inference)
// if there is none, and reports degraded. It only reads telemetry, so a failing
// model never blocks or drops the core telemetry path.
type Engine struct {
	mu       sync.Mutex
	active   *Model
	fallback *Model
	windows  map[string]*Window // keyed by asset + "\x00" + measurement
	units    map[string]string  // same key -> bound unit, precomputed per model
	ran      map[string]uint64  // per-window Accepted() at the last successful run
	failures int
	status   func(StatusEvent)
	now      func() time.Time
}

// NewEngine activates model, keeping fallback (which may be nil) as the
// rollback target. The fallback must be an earlier version of the same model.
func NewEngine(model, fallback *Model, status func(StatusEvent)) (*Engine, error) {
	if model == nil {
		return nil, errors.New("engine requires an active model")
	}
	if fallback != nil {
		m, f := model.Bundle.Manifest, fallback.Bundle.Manifest
		if f.ModelID != m.ModelID || m.Budgets.RollbackToVersion == nil || f.ModelVersion != *m.Budgets.RollbackToVersion {
			return nil, fmt.Errorf("fallback %s@%s is not the declared rollback of %s@%s", f.ModelID, f.ModelVersion, m.ModelID, m.ModelVersion)
		}
	}
	if status == nil {
		status = func(StatusEvent) {}
	}
	e := &Engine{fallback: fallback, status: status, now: func() time.Time { return time.Now().UTC() }}
	e.activate(model)
	return e, nil
}

func windowKey(asset, measurement string) string { return asset + "\x00" + measurement }

// activate swaps in a model and pre-allocates its windows. Called with mu held
// (or before the engine is shared).
func (e *Engine) activate(model *Model) {
	e.active = model
	e.failures = 0
	e.windows = map[string]*Window{}
	e.units = map[string]string{}
	e.ran = map[string]uint64{}
	if model == nil {
		return
	}
	size := model.Bundle.Manifest.Inputs.WindowSizeSamples
	for _, b := range model.Bundle.Manifest.Inputs.Bindings {
		key := windowKey(b.Asset, b.Measurement)
		e.windows[key] = NewWindow(size)
		e.units[key] = b.Unit
	}
}

// Observe feeds one telemetry sample. Samples for unbound measurements are
// ignored. It reports whether the sample was accepted into a window.
func (e *Engine) Observe(asset, measurement string, s Sample) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.push(asset, measurement, s)
}

// push appends to a bound window and counts accepted samples. Called with mu held.
func (e *Engine) push(asset, measurement string, s Sample) bool {
	w := e.windows[windowKey(asset, measurement)]
	return w != nil && w.Push(s)
}

// Active reports the active model as model_id@model_version, or "standby".
func (e *Engine) Active() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return describe(e.active)
}

func describe(m *Model) string {
	if m == nil {
		return "standby"
	}
	return m.Bundle.Manifest.ModelID + "@" + m.Bundle.Manifest.ModelVersion
}

// Step runs one inference if every bound window is ready. It returns
// (nil, nil) when windows are not ready or the engine is in standby.
func (e *Engine) Step(ctx context.Context) (*Output, error) {
	e.mu.Lock()
	model := e.active
	if model == nil {
		e.mu.Unlock()
		return nil, nil
	}
	// Every bound window must have a new sample since the last successful run:
	// re-scoring a stalled stream would republish stale data as a new window.
	snapshot := map[string]uint64{}
	for key, w := range e.windows {
		if w.Accepted() == e.ran[key] {
			e.mu.Unlock()
			return nil, nil
		}
		snapshot[key] = w.Accepted()
	}
	manifest := model.Bundle.Manifest
	span := time.Duration(manifest.Inputs.WindowDurationMS) * time.Millisecond
	in := Input{Windows: map[string][]float64{}, GoodRatios: map[string]float64{}}
	var start, end time.Time
	for _, b := range manifest.Inputs.Bindings {
		key := windowKey(b.Asset, b.Measurement)
		w := e.windows[key]
		if !w.Ready(span, manifest.Inputs.MinGoodRatio) {
			e.mu.Unlock()
			return nil, nil
		}
		// The runner may keep its input, so each window gets its own copy.
		values := w.GoodValues(make([]float64, 0, manifest.Inputs.WindowSizeSamples))
		in.Windows[b.Measurement] = values
		in.GoodRatios[b.Measurement] = float64(len(values)) / float64(manifest.Inputs.WindowSizeSamples)
		first, last := w.Bounds()
		if start.IsZero() || first.Before(start) {
			start = first
		}
		if last.After(end) {
			end = last
		}
	}
	e.mu.Unlock()

	budget := time.Duration(manifest.Budgets.MaxInferenceLatencyMS) * time.Millisecond
	e.mu.Lock()
	if model.busy != nil {
		select {
		case <-model.busy:
			model.busy = nil
		default:
			// A previous call overran its budget and is still running. Never
			// start another: at most one abandoned call exists, and the runner
			// is only ever called from one goroutine at a time.
			e.mu.Unlock()
			return nil, e.fail(model, errBudget)
		}
	}
	done := make(chan struct{})
	model.busy = done
	e.mu.Unlock()
	callCtx, cancel := context.WithTimeout(ctx, budget)
	result, err := runWithDeadline(callCtx, model.Runner, in, done)
	cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != model {
		return nil, nil // swapped while running; discard the stale result
	}
	if err != nil {
		// A failed run published nothing, so the same window may be retried;
		// retries are what accumulate toward the failure budget.
		return nil, e.failLocked(model, err)
	}
	e.failures = 0
	e.ran = snapshot // only a successful run consumes its window
	return &Output{Manifest: manifest, ModelID: manifest.ModelID, ModelVersion: manifest.ModelVersion, Result: result, WindowStart: start, WindowEnd: end}, nil
}

// errBudget marks a latency-budget breach.
var errBudget = errors.New("inference exceeded latency budget")

// runWithDeadline enforces the latency budget even on a runner that ignores
// ctx: the call is abandoned when the deadline passes. done is closed when the
// runner actually returns, so the engine never overlaps calls.
func runWithDeadline(ctx context.Context, runner Runner, in Input, done chan struct{}) (Result, error) {
	type outcome struct {
		result Result
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		defer close(done)
		result, err := runner.Infer(ctx, in)
		finished <- outcome{result, err}
	}()
	select {
	case o := <-finished:
		return o.result, o.err
	case <-ctx.Done():
		return Result{}, fmt.Errorf("%w: %v", errBudget, ctx.Err())
	}
}

// fail records a failure for model and rolls back once the budget is spent.
func (e *Engine) fail(model *Model, err error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.failLocked(model, err)
}

// failLocked is fail with mu held.
func (e *Engine) failLocked(model *Model, err error) error {
	if e.active != model {
		return err
	}
	e.failures++
	if e.failures >= model.Bundle.Manifest.Budgets.MaxConsecutiveFailures {
		e.rollback(model, err)
	}
	return err
}

// rollback swaps the failed model for the fallback or standby. Called with mu
// held.
func (e *Engine) rollback(failed *Model, cause error) {
	next := e.fallback
	e.fallback = nil // a rolled-back model is not rolled back again
	_ = failed.Runner.Close()
	e.activate(next)
	reason := "failure-budget"
	if errors.Is(cause, errBudget) {
		reason = "latency-budget"
	}
	e.status(StatusEvent{State: StateDegraded, Reason: fmt.Sprintf("%s: %s rolled back to %s", reason, describe(failed), describe(next)), Model: describe(next), At: e.now()})
}

// ObserveTelemetry validates one telemetry payload received on topic and feeds
// every bound measurement in it. The payload must pass the telemetry contract
// and its asset must match the topic through the registry, so a publisher
// allowed on one asset's topic cannot feed another asset's model. A sample
// counts as good only when its quality is "good", its value is numeric and its
// unit matches the binding, so a mis-scaled series never feeds a model.
func (e *Engine) ObserveTelemetry(validator interface {
	Validate(kind string, raw []byte) (map[string]any, error)
}, registry interface {
	ValidateTopic(topic, assetID string) error
}, topic string, raw []byte) (int, error) {
	payload, err := validator.Validate("telemetry", raw)
	if err != nil {
		return 0, err
	}
	asset, _ := payload["asset_id"].(string)
	if err := registry.ValidateTopic(topic, asset); err != nil {
		return 0, err
	}
	observed, err := time.Parse("2006-01-02T15:04:05.000Z", payload["observed_at"].(string))
	if err != nil {
		return 0, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	accepted := 0
	for name, raw := range payload["measurements"].(map[string]any) {
		unit, bound := e.units[windowKey(asset, name)]
		if !bound {
			continue
		}
		m := raw.(map[string]any)
		value, numeric := toFloat(m["value"])
		good := numeric && m["quality"] == "good" && m["unit"] == unit
		if e.push(asset, name, Sample{Value: value, At: observed, Good: good}) {
			accepted++
		}
	}
	return accepted, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case interface{ Float64() (float64, error) }:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// InputAssets returns every asset whose telemetry the engine may need, across
// the active and fallback models, so a rollback never leaves windows unfed.
func (e *Engine) InputAssets() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	var assets []string
	for _, m := range []*Model{e.active, e.fallback} {
		if m == nil {
			continue
		}
		for _, b := range m.Bundle.Manifest.Inputs.Bindings {
			if !seen[b.Asset] {
				seen[b.Asset] = true
				assets = append(assets, b.Asset)
			}
		}
	}
	return assets
}

// ReportFailure counts a failure that happened after a successful run, such as
// an output that could not be published, against the model that produced out.
func (e *Engine) ReportFailure(out *Output, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil || e.active.Bundle.Manifest.ModelID != out.ModelID || e.active.Bundle.Manifest.ModelVersion != out.ModelVersion {
		return
	}
	e.failLocked(e.active, err)
}

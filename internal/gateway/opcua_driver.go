package gateway

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"time"

	"github.com/awcullen/opcua/ua"
	"github.com/openump/Hades/internal/opcuapilot"
)

type opcuaBatchReader interface {
	Read(ctx context.Context) ([]ua.DataValue, error)
	Close()
}

type opcuaSubscriptionReader interface {
	Next(ctx context.Context) ([]ua.DataValue, error)
}

type OPCUADriver struct {
	endpoint     string
	signals      []opcuapilot.Signal
	reader       opcuaBatchReader
	opener       func(ctx context.Context) (opcuaBatchReader, error)
	subscriber   opcuaSubscriptionReader
	options      DriverOptions
	session      *PersistentSession
	guardrail    *EndpointGuardrail
	statusMu     sync.Mutex
	lastStatus   ProducerStatus
	deadbandMu   sync.Mutex
	lastReported map[string]float64
	lastValues   map[string]any
	lastAt       map[string]time.Time
}

func OpenOPCUADriver(ctx context.Context, endpoint, pki string, options DriverOptions) (*OPCUADriver, error) {
	return OpenOPCUADriverForSignals(ctx, endpoint, pki, opcuapilot.Signals[:], options)
}

func OpenOPCUADriverForSignals(ctx context.Context, endpoint, pki string, signals []opcuapilot.Signal, options DriverOptions) (*OPCUADriver, error) {
	reader, err := opcuapilot.DialWithSignals(ctx, endpoint, pki, "client", signals)
	if err != nil && !errors.Is(err, syscall.ECONNREFUSED) {
		return nil, err
	}
	// An offline source is a bad/null acquisition, not a fatal service startup.
	// Authentication, trust and configuration errors still fail closed.
	if err != nil {
		reader = nil
	}
	driver := NewOPCUADriverWithSource(endpoint, nil, nil, signals, options)
	if reader != nil {
		driver.reader = reader
	}
	driver.opener = func(ctx context.Context) (opcuaBatchReader, error) {
		return opcuapilot.DialWithSignals(ctx, endpoint, pki, "client", signals)
	}
	return driver, nil
}

func NewOPCUADriverWithSource(endpoint string, reader opcuaBatchReader, subscriber opcuaSubscriptionReader, signals []opcuapilot.Signal, options DriverOptions) *OPCUADriver {
	if len(signals) == 0 {
		signals = opcuapilot.Signals[:]
	}
	options = normalizeDriverOptions(options)
	return &OPCUADriver{
		endpoint:     endpoint,
		signals:      append([]opcuapilot.Signal(nil), signals...),
		reader:       reader,
		subscriber:   subscriber,
		options:      options,
		session:      NewPersistentSession(endpoint),
		guardrail:    NewEndpointGuardrail(endpoint, options.RatePerSecond, options.Burst, options.MaxInFlight),
		lastStatus:   ProducerStateGood,
		lastReported: make(map[string]float64, len(signals)),
		lastValues:   make(map[string]any, len(signals)),
		lastAt:       make(map[string]time.Time, len(signals)),
	}
}

func (d *OPCUADriver) Poll(ctx context.Context) (map[string]Reading, time.Time, string) {
	if err := d.guardrail.Enter(); err != nil {
		event := d.guardrail.RecordFailure("scan-protection")
		d.emitStatus(event)
		return d.failureReadings("scan-protection"), d.options.Now(), "scan-protection"
	}
	defer d.guardrail.Leave()

	readCtx, cancel := context.WithTimeout(ctx, d.options.ReadTimeout)
	defer cancel()

	values, err := d.next(readCtx)
	if err != nil {
		reason := "read-error"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			reason = "read-timeout"
		}
		event := d.guardrail.RecordFailure(reason)
		d.emitStatus(event)
		return d.failureReadings(reason), d.options.Now(), reason
	}

	stamp := time.Time{}
	out := make(map[string]Reading, len(d.signals))
	for index, signal := range d.signals {
		reading, observed, _, ext := opcuapilot.MapValue(signal, values[index], d.options.Now())
		if reader, ok := d.reader.(*opcuapilot.Reader); ok {
			ext["opcua.namespace_index"] = reader.Namespace
		}
		current := Reading{
			Value:      reading["value"],
			Unit:       signal.Unit,
			Quality:    reading["quality"].(string),
			ObservedAt: observed,
			Extensions: ext,
		}
		if reason, ok := reading["reason"].(string); ok {
			current.Reason = reason
		}
		if current.Quality == "good" || current.Quality == "uncertain" {
			current.Value = d.applyDeadband(signal.Name, current.Value, observed)
		}
		out[signal.Name] = current
		if observed.After(stamp) {
			stamp = observed
		}
	}
	if stamp.IsZero() {
		stamp = d.options.Now()
	}
	d.session.RecordPoll(nil, "")
	recovery := d.guardrail.RecordSuccess()
	d.emitStatus(recovery)
	return out, stamp, ""
}

func (d *OPCUADriver) Close() error {
	if d.reader != nil {
		d.reader.Close()
		d.reader = nil
	}
	return nil
}

func (d *OPCUADriver) next(ctx context.Context) ([]ua.DataValue, error) {
	if d.subscriber != nil {
		return d.subscriber.Next(ctx)
	}
	reader, err := d.ensureReader(ctx)
	if err != nil {
		return nil, err
	}
	type readResult struct {
		values []ua.DataValue
		err    error
	}
	resultCh := make(chan readResult, 1)
	go func(activeReader opcuaBatchReader) {
		values, err := activeReader.Read(ctx)
		resultCh <- readResult{values: values, err: err}
	}(reader)
	select {
	case result := <-resultCh:
		return result.values, result.err
	case <-ctx.Done():
		d.invalidateReader(reader)
		return nil, ctx.Err()
	}
}

func (d *OPCUADriver) ensureReader(ctx context.Context) (opcuaBatchReader, error) {
	if d.reader != nil {
		return d.reader, nil
	}
	if d.opener == nil {
		return nil, errors.New("opcua reader unavailable")
	}
	reader, err := d.opener(ctx)
	if err != nil {
		return nil, err
	}
	d.reader = reader
	return reader, nil
}

func (d *OPCUADriver) invalidateReader(target opcuaBatchReader) {
	if target != nil {
		target.Close()
	}
	if d.reader == target {
		d.reader = nil
	}
}

func (d *OPCUADriver) failureReadings(reason string) map[string]Reading {
	out := make(map[string]Reading, len(d.signals))
	for _, signal := range d.signals {
		out[signal.Name] = Reading{Value: nil, Unit: signal.Unit, Quality: "bad", Reason: reason, ObservedAt: d.options.Now(), Extensions: map[string]any{
			"opcua.node_id":          signal.Node,
			"opcua.timestamp_origin": "gateway",
			"opcua.source_timestamp": nil,
			"opcua.server_timestamp": nil,
			"opcua.status_code":      nil,
		}}
	}
	return out
}

func (d *OPCUADriver) applyDeadband(name string, value any, now time.Time) any {
	cfg, ok := d.options.Deadbands[name]
	if !ok || cfg.MaxSilenceInterval <= 0 {
		return value
	}
	numeric, ok := asFloat64(value)
	if !ok {
		return value
	}
	d.deadbandMu.Lock()
	defer d.deadbandMu.Unlock()
	last, seen := d.lastReported[name]
	if !seen || now.Sub(d.lastAt[name]) >= cfg.MaxSilenceInterval {
		d.lastReported[name] = numeric
		d.lastValues[name] = value
		d.lastAt[name] = now
		return value
	}
	if absFloat64(numeric-last) <= float64(cfg.Epsilon) {
		return d.lastValues[name]
	}
	d.lastReported[name] = numeric
	d.lastValues[name] = value
	d.lastAt[name] = now
	return value
}

func (d *OPCUADriver) emitStatus(event ProducerStatusEvent) {
	if d.options.StatusReporter == nil {
		return
	}
	event.GatewayID = d.options.GatewayID
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	if event.State == d.lastStatus {
		return
	}
	d.lastStatus = event.State
	d.options.StatusReporter(event)
}

func asFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}

func absFloat64(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

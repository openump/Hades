package gateway

import (
	"context"
	"errors"
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

// closableSubscription is a subscription the driver created itself and must
// delete when the session is reset.
type closableSubscription interface {
	opcuaSubscriptionReader
	Close(ctx context.Context) error
}

type OPCUADriver struct {
	endpoint   string
	signals    []opcuapilot.Signal
	reader     opcuaBatchReader
	opener     func(ctx context.Context) (opcuaBatchReader, error)
	subscriber opcuaSubscriptionReader
	subscribe  func(ctx context.Context, reader opcuaBatchReader) (closableSubscription, error)
	owned      closableSubscription
	options    DriverOptions
	session    *PersistentSession
	guardrail  *EndpointGuardrail
	status     *statusEmitter
	deadbands  deadbandSet
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
	if driver.options.Subscribe {
		interval := float64(driver.options.PollInterval.Milliseconds())
		driver.subscribe = func(ctx context.Context, reader opcuaBatchReader) (closableSubscription, error) {
			pilot, ok := reader.(*opcuapilot.Reader)
			if !ok {
				return nil, errors.New("opcua subscriptions require a pilot reader")
			}
			return pilot.Subscribe(ctx, interval)
		}
	}
	return driver, nil
}

func NewOPCUADriverWithSource(endpoint string, reader opcuaBatchReader, subscriber opcuaSubscriptionReader, signals []opcuapilot.Signal, options DriverOptions) *OPCUADriver {
	if len(signals) == 0 {
		signals = opcuapilot.Signals[:]
	}
	options = normalizeDriverOptions(options)
	return &OPCUADriver{
		endpoint:   endpoint,
		signals:    append([]opcuapilot.Signal(nil), signals...),
		reader:     reader,
		subscriber: subscriber,
		options:    options,
		session:    NewPersistentSession(endpoint),
		guardrail:  NewEndpointGuardrail(endpoint, options.RatePerSecond, options.Burst, options.MaxInFlight),
		status:     newStatusEmitter(options),
		deadbands:  newDeadbandSet(options.Deadbands),
	}
}

func (d *OPCUADriver) Poll(ctx context.Context) (map[string]Reading, time.Time, string) {
	if err := d.guardrail.Enter(); err != nil {
		reason := guardReason(err)
		return d.deadbands.filter(d.failureReadings(reason), d.options.Now()), d.options.Now(), reason
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
		d.status.emit(event)
		return d.deadbands.filter(d.failureReadings(reason), d.options.Now()), d.options.Now(), reason
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
	d.status.emit(recovery)
	return d.deadbands.filter(out, d.options.Now()), stamp, ""
}

func (d *OPCUADriver) Close() error {
	d.dropSubscription()
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
	read := reader.Read
	if d.subscribe != nil {
		if d.owned == nil {
			subscription, err := d.subscribe(ctx, reader)
			if err != nil {
				d.invalidateReader(reader)
				return nil, err
			}
			d.owned = subscription
		}
		read = d.owned.Next
	}
	type readResult struct {
		values []ua.DataValue
		err    error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		values, err := read(ctx)
		resultCh <- readResult{values: values, err: err}
	}()
	select {
	case result := <-resultCh:
		if result.err != nil && d.owned != nil {
			d.invalidateReader(reader)
		}
		return result.values, result.err
	case <-ctx.Done():
		d.invalidateReader(reader)
		return nil, ctx.Err()
	}
}

// dropSubscription deletes a driver-owned subscription; it is recreated on the
// next poll.
func (d *OPCUADriver) dropSubscription() {
	if d.owned == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = d.owned.Close(ctx)
	d.owned = nil
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
	if d.owned != nil {
		// Stop the publish loop before its session goes away.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_ = d.owned.Close(ctx)
		cancel()
		d.owned = nil
	}
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

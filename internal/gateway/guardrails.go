package gateway

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// ProducerStatus identifies the health state emitted when a controller is
// degraded or offline.
type ProducerStatus string

const (
	ProducerStateGood     ProducerStatus = "good"
	ProducerStateDegraded ProducerStatus = "degraded"
	ProducerStateOffline  ProducerStatus = "offline"
)

// ProducerStatusEvent is the gateway-side health signal emitted when a PLC
// stalls, resets, or becomes slow enough to violate the scan-cycle budget.
type ProducerStatusEvent struct {
	GatewayID string
	Endpoint  string
	State     ProducerStatus
	Reason    string
	At        time.Time
}

// ReadRequestPlan describes one contiguous PLC read operation produced by the
// coalescer before dispatch to a protocol driver.
type ReadRequestPlan struct {
	StartAddress uint16
	EndAddress   uint16
	Registers    []RegisterMapping
}

// CoalesceAdjacentReads groups adjacent register addresses into a single read PDU.
// This keeps legacy PLC communication processors from being hammered with one tiny
// read per tag while preserving read-only semantics.
func CoalesceAdjacentReads(registers []RegisterMapping) []ReadRequestPlan {
	if len(registers) == 0 {
		return nil
	}
	cloned := append([]RegisterMapping(nil), registers...)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Address < cloned[j].Address })
	plans := make([]ReadRequestPlan, 0, len(cloned))
	current := ReadRequestPlan{StartAddress: cloned[0].Address, EndAddress: cloned[0].Address, Registers: []RegisterMapping{cloned[0]}}
	for _, reg := range cloned[1:] {
		if reg.Address == current.EndAddress+1 {
			current.EndAddress = reg.Address
			current.Registers = append(current.Registers, reg)
			continue
		}
		plans = append(plans, current)
		current = ReadRequestPlan{StartAddress: reg.Address, EndAddress: reg.Address, Registers: []RegisterMapping{reg}}
	}
	plans = append(plans, current)
	return plans
}

// TokenBucket provides a simple rate limiter per PLC endpoint.
type TokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func NewTokenBucket(ratePerSecond float64, burst int) *TokenBucket {
	if ratePerSecond <= 0 {
		ratePerSecond = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &TokenBucket{rate: ratePerSecond, burst: float64(burst), tokens: float64(burst), last: time.Now().UTC()}
}

func (tb *TokenBucket) Acquire() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	now := time.Now().UTC()
	if tb.last.IsZero() {
		tb.last = now
	}
	elapsed := now.Sub(tb.last).Seconds()
	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.burst {
		tb.tokens = tb.burst
	}
	tb.last = now
	if tb.tokens >= 1 {
		tb.tokens -= 1
		return true
	}
	return false
}

// CircuitBreaker prevents a stalled controller from being hammered forever.
// After the backoff window it admits a single half-open probe; a failed probe
// reopens the breaker with a doubled backoff, a successful one closes it.
type CircuitBreaker struct {
	mu             sync.Mutex
	state          ProducerStatus
	threshold      int
	failures       int
	openUntil      time.Time
	currentBackoff time.Duration
	maxBackoff     time.Duration
	resetAfter     time.Duration
	probing        bool
	now            func() time.Time
}

func NewCircuitBreaker(threshold int, baseBackoff, maxBackoff time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 2
	}
	if baseBackoff <= 0 {
		baseBackoff = 250 * time.Millisecond
	}
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}
	return &CircuitBreaker{threshold: threshold, currentBackoff: baseBackoff, maxBackoff: maxBackoff, resetAfter: baseBackoff, state: ProducerStateGood, now: func() time.Time { return time.Now().UTC() }}
}

// BeforeRequest reports whether a request may be sent. While the breaker is open
// it refuses requests until the backoff window has elapsed, then admits exactly
// one probe until that probe's outcome is recorded.
func (cb *CircuitBreaker) BeforeRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state != ProducerStateOffline {
		return true
	}
	if cb.probing || cb.now().Before(cb.openUntil) {
		return false
	}
	cb.probing = true
	return true
}

// cancelProbe releases a probe admitted by BeforeRequest that was never sent.
func (cb *CircuitBreaker) cancelProbe() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.probing = false
}

func (cb *CircuitBreaker) RecordFailure() ProducerStatus {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.probing = false
	cb.failures++
	if cb.state == ProducerStateOffline || cb.failures >= cb.threshold {
		cb.open()
	} else {
		cb.state = ProducerStateDegraded
	}
	return cb.state
}

func (cb *CircuitBreaker) open() {
	cb.state = ProducerStateOffline
	cb.openUntil = cb.now().Add(cb.currentBackoff)
	cb.currentBackoff = min(cb.currentBackoff*2, cb.maxBackoff)
}

func (cb *CircuitBreaker) RecordSuccess() ProducerStatus {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = ProducerStateGood
	cb.probing = false
	cb.failures = 0
	cb.currentBackoff = cb.resetAfter
	cb.openUntil = time.Time{}
	return cb.state
}

var (
	// ErrCircuitOpen means the endpoint is in circuit-breaker backoff.
	ErrCircuitOpen = errors.New("circuit-open")
	// ErrScanProtection means the gateway skipped a read to stay inside the
	// endpoint's rate or concurrency budget. It is not a controller failure.
	ErrScanProtection = errors.New("scan-protection")
)

// EndpointGuardrail enforces a per-PLC rate bucket and concurrency budget.
type EndpointGuardrail struct {
	mu            sync.Mutex
	endpoint      string
	bucket        *TokenBucket
	inflight      int
	maxInFlight   int
	breaker       *CircuitBreaker
	lastStatus    ProducerStatusEvent
	lastStatusSet bool
}

func NewEndpointGuardrail(endpoint string, ratePerSecond float64, burst int, maxInFlight int) *EndpointGuardrail {
	if maxInFlight <= 0 {
		maxInFlight = 1
	}
	return &EndpointGuardrail{
		endpoint:    endpoint,
		bucket:      NewTokenBucket(ratePerSecond, burst),
		maxInFlight: maxInFlight,
		breaker:     NewCircuitBreaker(2, 250*time.Millisecond, 30*time.Second),
	}
}

func (g *EndpointGuardrail) Enter() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maxInFlight > 0 && g.inflight >= g.maxInFlight {
		return fmt.Errorf("endpoint %s already has %d in-flight request(s): %w", g.endpoint, g.inflight, ErrScanProtection)
	}
	if g.breaker != nil && !g.breaker.BeforeRequest() {
		return fmt.Errorf("endpoint %s is in circuit-breaker backoff: %w", g.endpoint, ErrCircuitOpen)
	}
	if g.bucket != nil && !g.bucket.Acquire() {
		if g.breaker != nil {
			g.breaker.cancelProbe()
		}
		return fmt.Errorf("endpoint %s exceeded the token bucket budget: %w", g.endpoint, ErrScanProtection)
	}
	g.inflight++
	return nil
}

// guardReason maps an Enter rejection to the reading reason reported for the
// skipped poll.
func guardReason(err error) string {
	if errors.Is(err, ErrCircuitOpen) {
		return "circuit-open"
	}
	return "scan-protection"
}

func (g *EndpointGuardrail) Leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight > 0 {
		g.inflight--
	}
}

func (g *EndpointGuardrail) RecordFailure(reason string) ProducerStatusEvent {
	state := g.breaker.RecordFailure()
	event := ProducerStatusEvent{Endpoint: g.endpoint, State: state, Reason: reason, At: time.Now().UTC()}
	g.mu.Lock()
	g.lastStatus = event
	g.lastStatusSet = true
	g.mu.Unlock()
	return event
}

func (g *EndpointGuardrail) RecordSuccess() ProducerStatusEvent {
	state := g.breaker.RecordSuccess()
	event := ProducerStatusEvent{Endpoint: g.endpoint, State: state, Reason: "healthy", At: time.Now().UTC()}
	g.mu.Lock()
	g.lastStatus = event
	g.lastStatusSet = true
	g.mu.Unlock()
	return event
}

// DeadbandFilter implements report-by-exception: a value is reported only when it
// moves more than epsilon from the last reported value, or as a heartbeat once
// maxSilence has passed since the last report.
type DeadbandFilter struct {
	mu           sync.Mutex
	epsilon      float64
	maxSilence   time.Duration
	reported     bool
	lastValue    float64
	lastQuality  string
	lastReported time.Time
}

func NewDeadbandFilter(epsilon float64, maxSilence time.Duration) *DeadbandFilter {
	if epsilon < 0 {
		epsilon = 0
	}
	if maxSilence <= 0 {
		maxSilence = time.Minute
	}
	return &DeadbandFilter{epsilon: epsilon, maxSilence: maxSilence}
}

// Apply reports whether value should be emitted at now.
func (f *DeadbandFilter) Apply(value float64, now time.Time) bool {
	return f.apply(value, "", now)
}

// apply additionally reports any quality change, so a good→uncertain
// transition is never hidden by an unchanged value. now must be the gateway
// clock; device timestamps can stall or move backwards.
func (f *DeadbandFilter) apply(value float64, quality string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	silence := now.Sub(f.lastReported)
	if f.reported && quality == f.lastQuality && silence >= 0 && silence < f.maxSilence && math.Abs(value-f.lastValue) <= f.epsilon {
		return false
	}
	f.reported = true
	f.lastValue = value
	f.lastQuality = quality
	f.lastReported = now
	return true
}

// Reset forgets the last reported value so the next good value is always
// emitted, e.g. after the signal went bad.
func (f *DeadbandFilter) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported = false
}

// deadbandSet holds the configured per-signal filters of one driver.
type deadbandSet map[string]*DeadbandFilter

// newDeadbandSet builds filters for configs with a positive MaxSilenceInterval;
// a zero interval leaves the signal unfiltered.
func newDeadbandSet(configs map[string]DeadbandConfig) deadbandSet {
	set := make(deadbandSet, len(configs))
	for name, cfg := range configs {
		if cfg.MaxSilenceInterval > 0 {
			set[name] = NewDeadbandFilter(cfg.Epsilon, cfg.MaxSilenceInterval)
		}
	}
	return set
}

// filter removes readings suppressed by their deadband, timed by the gateway
// poll time now. Bad readings are always kept and reset the filter so recovery
// is reported immediately.
func (s deadbandSet) filter(readings map[string]Reading, now time.Time) map[string]Reading {
	if len(s) == 0 {
		return readings
	}
	for name, reading := range readings {
		filter, ok := s[name]
		if !ok {
			continue
		}
		if reading.Quality == "bad" {
			filter.Reset()
			continue
		}
		numeric, ok := asFloat64(reading.Value)
		if !ok {
			continue
		}
		if !filter.apply(numeric, reading.Quality, now) {
			delete(readings, name)
		}
	}
	return readings
}

// statusEmitter reports producer_status transitions, deduplicating repeats of
// the current state.
type statusEmitter struct {
	mu        sync.Mutex
	last      ProducerStatus
	gatewayID string
	report    func(ProducerStatusEvent)
}

func newStatusEmitter(options DriverOptions) *statusEmitter {
	return &statusEmitter{last: ProducerStateGood, gatewayID: options.GatewayID, report: options.StatusReporter}
}

func (e *statusEmitter) emit(event ProducerStatusEvent) {
	if e.report == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if event.State == e.last {
		return
	}
	e.last = event.State
	event.GatewayID = e.gatewayID
	e.report(event)
}

// PersistentSession models a reused read session across poll cycles so the gateway
// does not open a new child process for each tick.
type PersistentSession struct {
	mu        sync.Mutex
	SessionID string
	PollCount int
	LastSeen  time.Time
	LastError string
	Endpoint  string
	ReadPlan  []ReadRequestPlan
}

func NewPersistentSession(endpoint string) *PersistentSession {
	return &PersistentSession{SessionID: fmt.Sprintf("sess-%d", time.Now().UnixNano()), Endpoint: endpoint, LastSeen: time.Now().UTC()}
}

func (s *PersistentSession) RecordPoll(plan []ReadRequestPlan, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PollCount++
	s.ReadPlan = plan
	s.LastSeen = time.Now().UTC()
	s.LastError = reason
}

func (s *PersistentSession) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PollCount = 0
	s.ReadPlan = nil
	s.LastError = ""
}

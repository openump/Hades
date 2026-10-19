package gateway

import (
	"fmt"
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
type CircuitBreaker struct {
	mu             sync.Mutex
	state          ProducerStatus
	threshold      int
	openUntil      time.Time
	currentBackoff time.Duration
	maxBackoff     time.Duration
	resetAfter     time.Duration
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
	return &CircuitBreaker{threshold: threshold, currentBackoff: baseBackoff, maxBackoff: maxBackoff, resetAfter: baseBackoff, state: ProducerStateGood}
}

func (cb *CircuitBreaker) BeforeRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == ProducerStateOffline && time.Now().UTC().Before(cb.openUntil) {
		return false
	}
	if cb.state == ProducerStateOffline && time.Now().UTC().After(cb.openUntil) {
		cb.state = ProducerStateGood
		cb.currentBackoff = cb.resetAfter
	}
	return true
}

func (cb *CircuitBreaker) RecordFailure() ProducerStatus {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == ProducerStateGood {
		cb.state = ProducerStateDegraded
	} else if cb.state == ProducerStateDegraded {
		cb.state = ProducerStateOffline
		cb.openUntil = time.Now().UTC().Add(cb.currentBackoff)
		cb.currentBackoff *= 2
		if cb.currentBackoff > cb.maxBackoff {
			cb.currentBackoff = cb.maxBackoff
		}
	}
	return cb.state
}

func (cb *CircuitBreaker) RecordSuccess() ProducerStatus {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = ProducerStateGood
	cb.currentBackoff = cb.resetAfter
	cb.openUntil = time.Time{}
	return cb.state
}

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
	if g.breaker != nil && !g.breaker.BeforeRequest() {
		return fmt.Errorf("endpoint %s is in circuit-breaker backoff", g.endpoint)
	}
	if g.maxInFlight > 0 && g.inflight >= g.maxInFlight {
		return fmt.Errorf("endpoint %s already has %d in-flight request(s)", g.endpoint, g.inflight)
	}
	if g.bucket != nil && !g.bucket.Acquire() {
		return fmt.Errorf("endpoint %s exceeded the token bucket budget", g.endpoint)
	}
	g.inflight++
	return nil
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

// DeadbandFilter suppresses insignificant value changes while preserving a
// heartbeat when the controller goes quiet.
type DeadbandFilter struct {
	mu           sync.Mutex
	epsilon      uint16
	maxSilence   time.Duration
	lastValue    *uint16
	lastObserved time.Time
}

func NewDeadbandFilter(epsilon uint16, maxSilence time.Duration) *DeadbandFilter {
	if epsilon > 0xFFFF {
		epsilon = 0xFFFF
	}
	if maxSilence <= 0 {
		maxSilence = time.Minute
	}
	return &DeadbandFilter{epsilon: epsilon, maxSilence: maxSilence}
}

func (f *DeadbandFilter) Apply(value uint16, now time.Time) (uint16, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastValue == nil {
		f.lastValue = &value
		f.lastObserved = now
		return value, true
	}
	if now.Sub(f.lastObserved) >= f.maxSilence {
		f.lastValue = &value
		f.lastObserved = now
		return value, true
	}
	prev := *f.lastValue
	delta := uint16(0)
	if value > prev {
		delta = value - prev
	} else {
		delta = prev - value
	}
	if delta <= f.epsilon {
		return prev, false
	}
	f.lastValue = &value
	f.lastObserved = now
	return value, true
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

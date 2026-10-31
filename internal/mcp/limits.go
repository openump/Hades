package mcp

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Limiter is a per-caller token bucket. Each caller identity gets its own
// bucket, so one agent cannot exhaust another's budget or the historian.
type Limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter(ratePerSecond float64, burst int) *Limiter {
	return &Limiter{rate: ratePerSecond, burst: float64(burst), buckets: map[string]*bucket{}, now: time.Now}
}

// Allow spends one token for caller, refilling at the configured rate.
func (l *Limiter) Allow(caller string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[caller]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[caller] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// History query bounds. hades-history-proxy gates routes by identity but does
// not bound queries, so hades-mcp enforces them and builds the query itself:
// an agent never supplies PromQL, so it cannot express a wildcard or
// unbounded scan.
const (
	MaxHistoryWindow = 24 * time.Hour
	MinHistoryStep   = time.Second
	MaxHistoryPoints = 1440
)

// HistoryQuery is a bounded single-series history request.
type HistoryQuery struct {
	Asset       string
	Measurement string
	Unit        string
	Revision    string
	Start, End  time.Time
	Step        time.Duration
}

var labelValue = regexp.MustCompile(`^[a-zA-Z0-9:._-]{1,256}$`)

// Values builds the query_range parameters for exactly one series. Both label
// values are restricted to a safe character set and quoted, so they cannot
// inject a matcher, and the window, step and point count are bounded.
func (q HistoryQuery) Values(now time.Time) (url.Values, error) {
	switch {
	case !labelValue.MatchString(q.Asset) || !labelValue.MatchString(q.Measurement) || !labelValue.MatchString(q.Unit) || !labelValue.MatchString(q.Revision):
		return nil, fmt.Errorf("asset, measurement, unit and revision must be plain identifiers")
	case !q.End.After(q.Start):
		return nil, fmt.Errorf("history window must have end after start")
	case q.End.After(now.Add(time.Minute)):
		return nil, fmt.Errorf("history window may not end in the future")
	case q.End.Sub(q.Start) > MaxHistoryWindow:
		return nil, fmt.Errorf("history window exceeds %s", MaxHistoryWindow)
	case q.Step < MinHistoryStep:
		return nil, fmt.Errorf("history step must be at least %s", MinHistoryStep)
	case int(q.End.Sub(q.Start)/q.Step)+1 > MaxHistoryPoints:
		return nil, fmt.Errorf("history query exceeds %d points; widen the step", MaxHistoryPoints)
	}
	values := url.Values{}
	// hades_observation_value holds only good-quality numeric readings (see
	// docs/protocol/history-mapping.md); a gap means bad or uncertain, not zero.
	// All four identifying labels, so the query matches exactly one series even
	// across a unit change or a metadata revision bump.
	values.Set("query", fmt.Sprintf(`hades_observation_value{asset_id=%q,measurement=%q,unit=%q,metadata_revision=%q}`, q.Asset, q.Measurement, q.Unit, q.Revision))
	values.Set("start", strconv.FormatInt(q.Start.Unix(), 10))
	values.Set("end", strconv.FormatInt(q.End.Unix(), 10))
	values.Set("step", strconv.FormatInt(int64(q.Step/time.Second), 10))
	return values, nil
}

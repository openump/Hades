package mcp

import (
	"testing"
	"time"
)

func TestLimiterIsPerCaller(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := NewLimiter(1, 2)
	l.now = func() time.Time { return now }
	if !l.Allow("agent-a") || !l.Allow("agent-a") || l.Allow("agent-a") {
		t.Fatal("agent-a gets a burst of 2, then is limited")
	}
	if !l.Allow("agent-b") {
		t.Fatal("one caller's exhaustion must not limit another")
	}
	now = now.Add(time.Second)
	if !l.Allow("agent-a") {
		t.Fatal("tokens refill at the configured rate")
	}
}

func TestHistoryQueryIsBoundedAndServerBuilt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ok := HistoryQuery{Asset: "urn:openump:acme:berlin:press-04", Measurement: "motor_temperature", Unit: "Cel", Revision: "1", Start: now.Add(-time.Hour), End: now, Step: time.Minute}
	values, err := ok.Values(now)
	if err != nil {
		t.Fatal(err)
	}
	if q := values.Get("query"); q != `hades_observation_value{asset_id="urn:openump:acme:berlin:press-04",measurement="motor_temperature",unit="Cel",metadata_revision="1"}` {
		t.Fatal("query must select exactly one series:", q)
	}
	for name, q := range map[string]HistoryQuery{
		"wildcard injection": {Asset: `x"} or {__name__=~".+`, Measurement: "m", Start: now.Add(-time.Hour), End: now, Step: time.Minute},
		"regex measurement":  {Asset: "a", Measurement: ".*", Start: now.Add(-time.Hour), End: now, Step: time.Minute},
		"window too long":    {Asset: "a", Measurement: "m", Start: now.Add(-48 * time.Hour), End: now, Step: time.Hour},
		"too many points":    {Asset: "a", Measurement: "m", Start: now.Add(-24 * time.Hour), End: now, Step: time.Second},
		"sub-second step":    {Asset: "a", Measurement: "m", Start: now.Add(-time.Minute), End: now, Step: time.Millisecond},
		"inverted window":    {Asset: "a", Measurement: "m", Start: now, End: now.Add(-time.Hour), Step: time.Minute},
		"future window":      {Asset: "a", Measurement: "m", Start: now, End: now.Add(time.Hour), Step: time.Minute},
	} {
		if _, err := q.Values(now); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	// query_range returns window/step + 1 points: 24 h at 1 min is 1441, one too many.
	if _, err := (HistoryQuery{Asset: "a", Measurement: "m", Unit: "Cel", Revision: "1", Start: now.Add(-24 * time.Hour), End: now, Step: time.Minute}).Values(now); err == nil {
		t.Fatal("24 h at 1 min returns 1441 points and must be refused")
	}
	if _, err := (HistoryQuery{Asset: "a", Measurement: "m", Unit: "Cel", Revision: "1", Start: now.Add(-24 * time.Hour).Add(time.Minute), End: now, Step: time.Minute}).Values(now); err != nil {
		t.Fatal("exactly 1440 points is allowed:", err)
	}
}

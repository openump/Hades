package operations

import (
	"testing"
	"time"
)

func TestLatencyBoundaries(t *testing.T) {
	var h Latency
	for _, d := range []time.Duration{0, 100 * time.Microsecond, 101 * time.Microsecond, 2 * time.Second} {
		h.Observe(d)
	}
	if h.Counts[0] != 2 || h.Counts[1] != 1 || h.Counts[8] != 1 {
		t.Fatal(h.Counts)
	}
	snapshot := h.Snapshot()
	if snapshot["unit"] != "milliseconds" {
		t.Fatal(snapshot)
	}
}

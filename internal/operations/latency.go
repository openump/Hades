package operations

import "time"

// Latency stores disjoint fixed buckets; callers serialize Observe/Snapshot with
// their existing state lock. The final bucket has no finite upper bound.
type Latency struct{ Counts [9]uint64 }

var latencyBounds = [...]float64{0.1, 0.5, 1, 5, 10, 50, 100, 1000}

func (h *Latency) Observe(d time.Duration) {
	ms := float64(d) / float64(time.Millisecond)
	for i, upper := range latencyBounds {
		if ms <= upper {
			h.Counts[i]++
			return
		}
	}
	h.Counts[len(h.Counts)-1]++
}
func (h Latency) Snapshot() map[string]any {
	bounds := make([]any, 0, len(h.Counts))
	for _, upper := range latencyBounds {
		bounds = append(bounds, upper)
	}
	bounds = append(bounds, nil)
	return map[string]any{"unit": "milliseconds", "upper_bounds": bounds, "disjoint_counts": h.Counts, "last_bucket": "greater_than_1000ms", "scope": "process_lifetime"}
}

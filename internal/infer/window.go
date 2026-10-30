package infer

import "time"

// Sample is one observation of a bound measurement.
type Sample struct {
	Value float64
	At    time.Time
	Good  bool
}

// Window is a fixed-capacity ring of the most recent samples of one
// (asset, measurement). Storage is allocated once at construction; Push never
// allocates. Out-of-order samples are rejected so the window is always in time
// order.
type Window struct {
	values   []float64
	times    []time.Time
	good     []bool
	head     int
	size     int
	last     time.Time
	rejected uint64
	accepted uint64 // samples accepted since construction
}

func NewWindow(capacity int) *Window {
	if capacity < 1 {
		capacity = 1
	}
	return &Window{values: make([]float64, capacity), times: make([]time.Time, capacity), good: make([]bool, capacity)}
}

// Push appends a sample unless it is not newer than the latest one. Bad and
// uncertain samples are kept (so the good ratio is honest) but never feed a
// model.
func (w *Window) Push(s Sample) bool {
	if w.size > 0 && !s.At.After(w.last) {
		w.rejected++
		return false
	}
	w.values[w.head], w.times[w.head], w.good[w.head] = s.Value, s.At, s.Good
	w.head = (w.head + 1) % len(w.values)
	if w.size < len(w.values) {
		w.size++
	}
	w.last = s.At
	w.accepted++
	return true
}

// Accepted reports how many samples the window has accepted.
func (w *Window) Accepted() uint64 { return w.accepted }

// Ready reports whether the window is full, spans no more than maxSpan from
// its oldest to newest sample, and meets the good-sample ratio.
func (w *Window) Ready(maxSpan time.Duration, minGoodRatio float64) bool {
	if w.size < len(w.values) {
		return false
	}
	if w.last.Sub(w.times[w.head]) > maxSpan {
		return false
	}
	good := 0
	for _, ok := range w.good {
		if ok {
			good++
		}
	}
	return float64(good)/float64(len(w.good)) >= minGoodRatio
}

// GoodValues copies the good samples, oldest first, into dst and returns it.
// dst is reused across calls so steady-state inference does not allocate.
func (w *Window) GoodValues(dst []float64) []float64 {
	dst = dst[:0]
	start := (w.head - w.size + len(w.values)) % len(w.values)
	for i := 0; i < w.size; i++ {
		idx := (start + i) % len(w.values)
		if w.good[idx] {
			dst = append(dst, w.values[idx])
		}
	}
	return dst
}

// Bounds returns the oldest and newest sample times in the window.
func (w *Window) Bounds() (time.Time, time.Time) {
	start := (w.head - w.size + len(w.values)) % len(w.values)
	return w.times[start], w.last
}

func (w *Window) Rejected() uint64 { return w.rejected }

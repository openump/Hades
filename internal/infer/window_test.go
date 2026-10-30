package infer

import (
	"testing"
	"time"
)

func TestWindowKeepsNewestInOrderAndRejectsStale(t *testing.T) {
	w := NewWindow(3)
	base := time.Unix(1_800_000_000, 0)
	for i := 0; i < 5; i++ {
		w.Push(Sample{Value: float64(i), At: base.Add(time.Duration(i) * time.Second), Good: true})
	}
	if got := w.GoodValues(nil); len(got) != 3 || got[0] != 2 || got[2] != 4 {
		t.Fatal(got)
	}
	if w.Push(Sample{Value: 99, At: base.Add(2 * time.Second), Good: true}) || w.Push(Sample{Value: 99, At: base.Add(4 * time.Second), Good: true}) {
		t.Fatal("out-of-order and duplicate-time samples must be rejected")
	}
	if w.Rejected() != 2 {
		t.Fatal(w.Rejected())
	}
	if first, last := w.Bounds(); !first.Equal(base.Add(2*time.Second)) || !last.Equal(base.Add(4*time.Second)) {
		t.Fatal(first, last)
	}
}

func TestWindowReadinessHonorsSpanAndQuality(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	w := NewWindow(4)
	for i, good := range []bool{true, true, false, true} {
		w.Push(Sample{Value: float64(i), At: base.Add(time.Duration(i) * time.Second), Good: good})
	}
	if !w.Ready(10*time.Second, 0.75) {
		t.Fatal("3 of 4 good meets a 0.75 ratio")
	}
	if w.Ready(10*time.Second, 0.9) {
		t.Fatal("3 of 4 good misses a 0.9 ratio")
	}
	if w.Ready(2*time.Second, 0.5) {
		t.Fatal("a 3 s span exceeds a 2 s window duration")
	}
	if got := w.GoodValues(nil); len(got) != 3 {
		t.Fatal("bad samples must not feed a model", got)
	}
	if NewWindow(4).Ready(time.Hour, 0) {
		t.Fatal("a partial window is not ready")
	}
}

func TestWindowPushDoesNotAllocate(t *testing.T) {
	w := NewWindow(64)
	base := time.Unix(1_800_000_000, 0)
	i := 0
	buf := make([]float64, 0, 64)
	allocs := testing.AllocsPerRun(1000, func() {
		i++
		w.Push(Sample{Value: float64(i), At: base.Add(time.Duration(i) * time.Millisecond), Good: true})
		buf = w.GoodValues(buf)
	})
	if allocs != 0 {
		t.Fatalf("Push/GoodValues allocated %.1f times per call", allocs)
	}
}

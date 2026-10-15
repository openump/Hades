package main

import "testing"

func TestPercentilesDoNotMutateMeasurements(t *testing.T) {
	values := []float64{9, 1, 5, 3, 7}
	if percentile(values, .5) != 5 || percentile(values, .95) != 7 || values[0] != 9 {
		t.Fatal(values)
	}
	if percentile(nil, .99) != 0 {
		t.Fatal("empty percentile")
	}
}

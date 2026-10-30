package infer

import (
	"context"
	"math"
	"testing"
)

func run(t *testing.T, artifact string, windows map[string][]float64) Result {
	t.Helper()
	model, err := ParseStatisticalModel([]byte(artifact))
	if err != nil {
		t.Fatal(err)
	}
	ratios := map[string]float64{}
	for name := range windows {
		ratios[name] = 1
	}
	result, err := (&statisticalRunner{model: model}).Infer(context.Background(), Input{Windows: windows, GoodRatios: ratios})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func steady(n int, level, noise float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = level + noise*math.Sin(float64(i)*1.7) // deterministic small noise
	}
	return out
}

func TestZScoreFlagsSpikeNotNoise(t *testing.T) {
	artifact := `{"detectors":[{"kind":"zscore","measurement":"temp","scale":3}]}`
	calm := steady(60, 70, 0.2)
	if s := run(t, artifact, map[string][]float64{"temp": calm}).Score; s > 0.5 {
		t.Fatalf("steady noise scored %.2f", s)
	}
	spike := append(steady(59, 70, 0.2), 78)
	if s := run(t, artifact, map[string][]float64{"temp": spike}).Score; s < 0.95 {
		t.Fatalf("a 40-sigma spike scored only %.2f", s)
	}
}

func TestEWMADriftFlagsSlowThermalDrift(t *testing.T) {
	artifact := `{"detectors":[{"kind":"ewma_drift","measurement":"temp","alpha":0.2,"scale":1}]}`
	drift := make([]float64, 60)
	for i := range drift {
		drift[i] = 70 + 0.1*float64(i) + 0.05*math.Sin(float64(i)) // +6 Cel over the window
	}
	if s := run(t, artifact, map[string][]float64{"temp": drift}).Score; s < 0.7 {
		t.Fatalf("thermal drift scored only %.2f", s)
	}
	if s := run(t, artifact, map[string][]float64{"temp": steady(60, 70, 0.2)}).Score; s > 0.6 {
		t.Fatalf("steady noise scored %.2f as drift", s)
	}
}

func TestRateOfChangeFlagsStep(t *testing.T) {
	artifact := `{"detectors":[{"kind":"rate_of_change","measurement":"pressure","scale":5}]}`
	step := steady(60, 145, 0.3)
	for i := 40; i < 60; i++ {
		step[i] += 12
	}
	if s := run(t, artifact, map[string][]float64{"pressure": step}).Score; s < 0.9 {
		t.Fatalf("a pressure step scored only %.2f", s)
	}
}

func TestBandEnergyFlagsBearingFrequency(t *testing.T) {
	// A bearing defect adds a high-frequency tone to low-frequency machine motion.
	artifact := `{"detectors":[{"kind":"band_energy","measurement":"vibration","band_low":0.5,"band_high":1,"scale":0.2}]}`
	healthy, faulty := make([]float64, 64), make([]float64, 64)
	for i := range healthy {
		base := math.Sin(2 * math.Pi * 2 * float64(i) / 64) // low frequency
		healthy[i] = base
		faulty[i] = base + 0.8*math.Sin(2*math.Pi*24*float64(i)/64) // 24/32 of Nyquist
	}
	h := run(t, artifact, map[string][]float64{"vibration": healthy}).Score
	f := run(t, artifact, map[string][]float64{"vibration": faulty}).Score
	if h > 0.1 || f < 0.8 {
		t.Fatalf("healthy %.3f, faulty %.3f", h, f)
	}
}

func TestFFTMatchesDirectDFT(t *testing.T) {
	values := []float64{1, -2, 3.5, 0.25, -1, 4, 2, -3}
	x := make([]complex128, len(values))
	for i, v := range values {
		x[i] = complex(v, 0)
	}
	fft(x)
	for k := range values {
		var want complex128
		for n, v := range values {
			angle := -2 * math.Pi * float64(k*n) / float64(len(values))
			want += complex(v*math.Cos(angle), v*math.Sin(angle))
		}
		if math.Abs(real(x[k]-want)) > 1e-9 || math.Abs(imag(x[k]-want)) > 1e-9 {
			t.Fatalf("bin %d: %v, want %v", k, x[k], want)
		}
	}
}

func TestSoftSensorsAndDeterminism(t *testing.T) {
	artifact := `{"detectors":[{"kind":"zscore","measurement":"temp","scale":3}],
		"soft_sensors":[{"kind":"mean","measurement":"temp","output":"temp_mean"},
		{"kind":"band_energy_ratio","measurement":"temp","output":"wear","band_low":0.5,"band_high":1,"scale":100}]}`
	windows := map[string][]float64{"temp": steady(32, 70, 0.4)}
	a, b := run(t, artifact, windows), run(t, artifact, windows)
	if a.Score != b.Score || a.SoftSensors["wear"] != b.SoftSensors["wear"] {
		t.Fatal("inference must be deterministic")
	}
	if math.Abs(a.SoftSensors["temp_mean"]-70) > 0.1 || a.SoftSensors["wear"] < 0 || a.SoftSensors["wear"] > 100 {
		t.Fatal(a.SoftSensors)
	}
}

func TestStatisticalArtifactValidation(t *testing.T) {
	for _, artifact := range []string{
		`{}`,
		`{"detectors":[{"kind":"zscore","measurement":"t","scale":0}]}`,
		`{"detectors":[{"kind":"neural","measurement":"t","scale":1}]}`,
		`{"detectors":[{"kind":"ewma_drift","measurement":"t","scale":1,"alpha":1.5}]}`,
		`{"detectors":[{"kind":"band_energy","measurement":"t","scale":1,"band_low":0.8,"band_high":0.2}]}`,
		`{"detectors":[{"kind":"zscore","measurement":"t","scale":1}],"backdoor":true}`,
	} {
		if _, err := ParseStatisticalModel([]byte(artifact)); err == nil {
			t.Fatalf("expected %s to be rejected", artifact)
		}
	}
}

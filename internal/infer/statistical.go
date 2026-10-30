package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/cmplx"
)

// StatisticalModel is the signed statistical_v1 artifact. Each detector scores
// one bound measurement; the model's anomaly score is the maximum detector
// score, so any single strong signal raises the event.
type StatisticalModel struct {
	Detectors []Detector `json:"detectors"`
	// SoftSensors derive telemetry from a window, for example a drift estimate.
	SoftSensors []SoftSensor `json:"soft_sensors,omitempty"`
}

// Detector configures one anomaly scorer. Scale maps the raw statistic to a
// 0..1 score through 1 - exp(-raw/scale), so raw == scale scores ~0.63.
type Detector struct {
	Kind        string  `json:"kind"` // zscore, ewma_drift, rate_of_change, band_energy
	Measurement string  `json:"measurement"`
	Scale       float64 `json:"scale"`
	Alpha       float64 `json:"alpha,omitempty"`     // ewma_drift smoothing, 0..1
	BandLow     float64 `json:"band_low,omitempty"`  // band_energy, fraction of Nyquist
	BandHigh    float64 `json:"band_high,omitempty"` // band_energy, fraction of Nyquist
}

// SoftSensor emits one derived measurement per window.
type SoftSensor struct {
	Kind        string  `json:"kind"` // ewma, mean, band_energy_ratio
	Measurement string  `json:"measurement"`
	Output      string  `json:"output"`
	Alpha       float64 `json:"alpha,omitempty"`
	BandLow     float64 `json:"band_low,omitempty"`
	BandHigh    float64 `json:"band_high,omitempty"`
	Scale       float64 `json:"scale,omitempty"` // multiplies the result, e.g. to percent
}

// ParseStatisticalModel decodes and validates an artifact. Unknown fields and
// out-of-range parameters are rejected rather than defaulted.
func ParseStatisticalModel(raw []byte) (*StatisticalModel, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var model StatisticalModel
	if err := decoder.Decode(&model); err != nil {
		return nil, fmt.Errorf("statistical_v1 artifact: %w", err)
	}
	if len(model.Detectors) == 0 || len(model.Detectors) > 16 || len(model.SoftSensors) > 16 {
		return nil, fmt.Errorf("statistical_v1 artifact needs 1..16 detectors and at most 16 soft sensors")
	}
	for _, d := range model.Detectors {
		if !(d.Scale > 0) || math.IsInf(d.Scale, 0) {
			return nil, fmt.Errorf("detector %s on %s needs a positive finite scale", d.Kind, d.Measurement)
		}
		switch d.Kind {
		case "zscore", "rate_of_change":
		case "ewma_drift":
			if !(d.Alpha > 0 && d.Alpha <= 1) {
				return nil, fmt.Errorf("ewma_drift alpha must be in (0, 1]")
			}
		case "band_energy":
			if !(d.BandLow >= 0 && d.BandLow < d.BandHigh && d.BandHigh <= 1) {
				return nil, fmt.Errorf("band_energy band must satisfy 0 <= low < high <= 1")
			}
		default:
			return nil, fmt.Errorf("unknown detector kind %q", d.Kind)
		}
	}
	for _, s := range model.SoftSensors {
		switch s.Kind {
		case "mean":
		case "ewma":
			if !(s.Alpha > 0 && s.Alpha <= 1) {
				return nil, fmt.Errorf("ewma soft sensor alpha must be in (0, 1]")
			}
		case "band_energy_ratio":
			if !(s.BandLow >= 0 && s.BandLow < s.BandHigh && s.BandHigh <= 1) {
				return nil, fmt.Errorf("band_energy_ratio band must satisfy 0 <= low < high <= 1")
			}
		default:
			return nil, fmt.Errorf("unknown soft sensor kind %q", s.Kind)
		}
	}
	return &model, nil
}

// statisticalRunner executes a StatisticalModel. It is deterministic: the same
// windows always produce the same result.
type statisticalRunner struct {
	model *StatisticalModel
}

func (r *statisticalRunner) Infer(_ context.Context, in Input) (Result, error) {
	result := Result{SoftSensors: map[string]float64{}}
	contributions := map[string]float64{}
	for _, d := range r.model.Detectors {
		values := in.Windows[d.Measurement]
		if len(values) < 2 {
			return Result{}, fmt.Errorf("detector %s: window for %s has %d good samples", d.Kind, d.Measurement, len(values))
		}
		var raw float64
		switch d.Kind {
		case "zscore":
			raw = zscore(values)
		case "ewma_drift":
			raw = ewmaDrift(values, d.Alpha)
		case "rate_of_change":
			raw = rateOfChange(values)
		case "band_energy":
			raw = bandEnergyRatio(values, d.BandLow, d.BandHigh)
		}
		score := 1 - math.Exp(-raw/d.Scale)
		if math.IsNaN(score) {
			return Result{}, fmt.Errorf("detector %s on %s produced NaN", d.Kind, d.Measurement)
		}
		if score > contributions[d.Measurement] {
			contributions[d.Measurement] = score
		}
		result.Score = math.Max(result.Score, score)
	}
	for _, s := range r.model.SoftSensors {
		values := in.Windows[s.Measurement]
		if len(values) == 0 {
			return Result{}, fmt.Errorf("soft sensor %s: no samples for %s", s.Output, s.Measurement)
		}
		var v float64
		switch s.Kind {
		case "mean":
			v = mean(values)
		case "ewma":
			v = ewma(values, s.Alpha)
		case "band_energy_ratio":
			v = bandEnergyRatio(values, s.BandLow, s.BandHigh)
		}
		if s.Scale != 0 {
			v *= s.Scale
		}
		result.SoftSensors[s.Output] = v
	}
	result.Contributions = contributions
	result.Confidence = confidence(in)
	return result, nil
}

func (r *statisticalRunner) Close() error { return nil }

func mean(values []float64) float64 {
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stddev(values []float64, m float64) float64 {
	sum := 0.0
	for _, v := range values {
		sum += (v - m) * (v - m)
	}
	return math.Sqrt(sum / float64(len(values)))
}

// zscore is how many standard deviations the newest sample sits from the mean
// of the earlier samples.
func zscore(values []float64) float64 {
	history := values[:len(values)-1]
	m := mean(history)
	sd := stddev(history, m)
	if sd == 0 {
		if values[len(values)-1] == m {
			return 0
		}
		return math.Inf(1)
	}
	return math.Abs(values[len(values)-1]-m) / sd
}

func ewma(values []float64, alpha float64) float64 {
	e := values[0]
	for _, v := range values[1:] {
		e = alpha*v + (1-alpha)*e
	}
	return e
}

// ewmaDrift is how far the smoothed level has moved from the window's start,
// relative to the window's spread; it rises for slow thermal or process drift
// that a z-score on the newest sample misses.
func ewmaDrift(values []float64, alpha float64) float64 {
	m := mean(values)
	sd := stddev(values, m)
	if sd == 0 {
		return 0
	}
	return math.Abs(ewma(values, alpha)-values[0]) / sd
}

// rateOfChange is the largest step between consecutive samples relative to
// the window's mean absolute step.
func rateOfChange(values []float64) float64 {
	steps := make([]float64, 0, len(values)-1)
	for i := 1; i < len(values); i++ {
		steps = append(steps, math.Abs(values[i]-values[i-1]))
	}
	typical := mean(steps)
	peak := 0.0
	for _, s := range steps {
		peak = math.Max(peak, s)
	}
	if typical == 0 {
		return 0
	}
	return peak / typical
}

// bandEnergyRatio is the fraction of non-DC spectral energy in the band
// [low, high) of the Nyquist frequency, from a radix-2 FFT of the mean-removed
// window (zero-padded to a power of two).
func bandEnergyRatio(values []float64, low, high float64) float64 {
	n := 1
	for n < len(values) {
		n <<= 1
	}
	m := mean(values)
	x := make([]complex128, n)
	for i, v := range values {
		x[i] = complex(v-m, 0)
	}
	fft(x)
	total, band := 0.0, 0.0
	for k := 1; k <= n/2; k++ {
		energy := real(x[k])*real(x[k]) + imag(x[k])*imag(x[k])
		total += energy
		f := float64(k) / float64(n/2)
		if f >= low && f < high {
			band += energy
		}
	}
	if total == 0 {
		return 0
	}
	return band / total
}

// fft is an in-place iterative radix-2 Cooley-Tukey transform; len(x) must be
// a power of two.
func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		step := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u, v := x[start+k], w*x[start+k+size/2]
				x[start+k], x[start+k+size/2] = u+v, u-v
				w *= step
			}
		}
	}
}

// confidence is the mean good-sample ratio across the model's windows.
func confidence(in Input) float64 {
	if len(in.GoodRatios) == 0 {
		return 0
	}
	sum := 0.0
	for _, r := range in.GoodRatios {
		sum += r
	}
	return sum / float64(len(in.GoodRatios))
}

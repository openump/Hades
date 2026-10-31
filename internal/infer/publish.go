package infer

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/openump/Hades/internal/sample"
)

// uuidV7 returns an RFC 9562 version 7 UUID for at: 48-bit Unix milliseconds,
// version 7, the RFC variant and 74 random bits. Event identities require v7.
// UUIDv7 returns an RFC 9562 version 7 UUID for at.
func UUIDv7(at time.Time) string { return uuidV7(at) }

func uuidV7(at time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	ms := uint64(at.UnixMilli())
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	b[6] = b[6]&0x0F | 0x70
	b[8] = b[8]&0x3F | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Publication is one payload and its MQTT topic.
type Publication struct {
	Topic   string
	Payload []byte
}

// EventPublication is kept as an alias for readability at call sites.
type EventPublication = Publication

// SoftSensorObservation is one soft-sensor value for the derived subtree.
type SoftSensorObservation struct {
	Asset       string
	Measurement string
	Value       float64
	Unit        string
}

// Publications maps one window's output to inference events (one per event
// binding, with severity from the binding's scores) and soft-sensor
// observations. topicPrefix maps an asset to its
// openump/v1/<enterprise>/<site>/<area>/<line>/<asset>/ topic prefix.
func Publications(m Manifest, out *Output, emitted time.Time, topicPrefix func(asset string) (string, error)) ([]EventPublication, []SoftSensorObservation, error) {
	var events []EventPublication
	var sensors []SoftSensorObservation
	score := clamp01(out.Result.Score)
	for _, binding := range m.Outputs {
		switch binding.Kind {
		case "inference-event":
			top := contributors(out.Result.Contributions)
			if len(top) == 0 {
				return nil, nil, fmt.Errorf("inference-event %s: runner reported no contributing measurement", binding.InferenceType)
			}
			threshold := binding.Severity.WarningScore
			severity := "info"
			switch {
			case score >= binding.Severity.CriticalScore:
				severity = "critical"
			case score >= threshold:
				severity = "warning"
			}
			prefix, err := topicPrefix(binding.Asset)
			if err != nil {
				return nil, nil, err
			}
			payload, err := json.Marshal(map[string]any{
				"schema_version":   "1.0",
				"event_id":         uuidV7(emitted),
				"timestamp":        emitted.UTC().Format("2006-01-02T15:04:05.000Z"),
				"asset":            binding.Asset,
				"inference_type":   binding.InferenceType,
				"model_id":         out.ModelID,
				"model_version":    out.ModelVersion,
				"anomaly_score":    score,
				"threshold":        threshold,
				"severity":         severity,
				"confidence":       clamp01(out.Result.Confidence),
				"window_start_ms":  out.WindowStart.UnixMilli(),
				"window_end_ms":    out.WindowEnd.UnixMilli(),
				"top_contributors": top,
			})
			if err != nil {
				return nil, nil, err
			}
			events = append(events, EventPublication{Topic: prefix + "event/" + binding.InferenceType, Payload: payload})
		case "telemetry":
			value, ok := out.Result.SoftSensors[binding.Measurement]
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				continue // the model produced no usable value this window
			}
			sensors = append(sensors, SoftSensorObservation{Asset: binding.Asset, Measurement: binding.Measurement, Value: value, Unit: binding.Unit})
		}
	}
	return events, sensors, nil
}

// contributors returns up to the 16 largest contributions, largest first.
// Only contributions to bound measurements with valid names are kept.
func contributors(contributions map[string]float64) []map[string]any {
	names := make([]string, 0, len(contributions))
	for name := range contributions {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if contributions[names[i]] != contributions[names[j]] {
			return contributions[names[i]] > contributions[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > 16 {
		names = names[:16]
	}
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"measurement": name, "contribution": clamp01(contributions[name])})
	}
	return out
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// TopicPrefix builds the asset topic prefix from the five registry hierarchy
// segments.
func TopicPrefix(enterprise, site, area, line, asset string) string {
	return strings.Join([]string{"openump", "v1", enterprise, site, area, line, asset}, "/") + "/"
}

// DerivedPublication is a soft-sensor value and its derived topic.
type DerivedPublication = Publication

// DerivedPublications maps soft-sensor observations to their own
// openump/v1/.../<asset>/derived/<measurement> topics. They are never device
// telemetry: the payload names the producing model and the window it summarizes,
// and observed_at is the window end, so a value always describes the data it
// was computed from rather than the publish time.
func DerivedPublications(sensors []SoftSensorObservation, out *Output, topicPrefix func(asset string) (string, error)) ([]DerivedPublication, error) {
	pubs := make([]DerivedPublication, 0, len(sensors))
	for _, o := range sensors {
		prefix, err := topicPrefix(o.Asset)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(map[string]any{
			"schema_version":  "1.0",
			"observation_id":  uuidV7(out.WindowEnd),
			"asset":           o.Asset,
			"measurement":     o.Measurement,
			"value":           o.Value,
			"unit":            o.Unit,
			"observed_at":     out.WindowEnd.UTC().Format(sample.Timestamp),
			"window_start_ms": out.WindowStart.UnixMilli(),
			"window_end_ms":   out.WindowEnd.UnixMilli(),
			"model_id":        out.ModelID,
			"model_version":   out.ModelVersion,
			"confidence":      clamp01(out.Result.Confidence),
		})
		if err != nil {
			return nil, err
		}
		pubs = append(pubs, DerivedPublication{Topic: prefix + "derived/" + o.Measurement, Payload: payload})
	}
	return pubs, nil
}

// Checker validates outgoing publications against the contracts before they
// reach the broker, so a misbehaving runner can never publish an invalid event
// or a mis-bound topic.
type Checker struct {
	Validator interface {
		Validate(kind string, raw []byte) (map[string]any, error)
	}
	Registry interface {
		ValidateInferenceEventTopic(topic string, event map[string]any) error
		ValidateDerivedTopic(topic, asset, measurement string) error
	}
}

func (c Checker) Event(pub Publication) error {
	event, err := c.Validator.Validate("inference-event", pub.Payload)
	if err != nil {
		return err
	}
	return c.Registry.ValidateInferenceEventTopic(pub.Topic, event)
}

func (c Checker) Derived(pub Publication) error {
	var value struct {
		Asset       string  `json:"asset"`
		Measurement string  `json:"measurement"`
		Value       float64 `json:"value"`
	}
	if err := json.Unmarshal(pub.Payload, &value); err != nil {
		return err
	}
	if math.IsNaN(value.Value) || math.IsInf(value.Value, 0) {
		return fmt.Errorf("derived %s: non-finite value", value.Measurement)
	}
	return c.Registry.ValidateDerivedTopic(pub.Topic, value.Asset, value.Measurement)
}

package infer

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/contracts"
)

func TestUUIDv7MatchesContractPattern(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	at := time.UnixMilli(1792152060050)
	seen := map[string]bool{}
	for range 1000 {
		id := uuidV7(at)
		if !pattern.MatchString(id) || seen[id] {
			t.Fatalf("invalid or repeated UUIDv7 %q", id)
		}
		seen[id] = true
	}
}

func registry(t *testing.T) *contracts.Registry {
	t.Helper()
	raw, _ := os.ReadFile("../../examples/contracts/assets.json")
	var assets []contracts.Asset
	json.Unmarshal(raw, &assets)
	r, err := contracts.NewRegistry(assets)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pressPrefix(asset string) (string, error) {
	return TopicPrefix("acme", "berlin", "assembly", "line-01", "press-04"), nil
}

// TestPublicationsValidateAgainstContracts runs every severity band through
// the real inference-event schema, its semantics and the topic binding.
func TestPublicationsValidateAgainstContracts(t *testing.T) {
	v := testValidator(t)
	reg := registry(t)
	key := testKey(0)
	dir := writeBundle(t, key, []byte("x"), nil)
	bundle, err := LoadBundle(dir, v, trustFor(key))
	if err != nil {
		t.Fatal(err)
	}
	start := time.UnixMilli(1792152000000)
	end := start.Add(time.Minute)
	for _, score := range []float64{0, 0.3, 0.7, 0.85, 0.9, 1} {
		out := &Output{ModelID: bundle.Manifest.ModelID, ModelVersion: bundle.Manifest.ModelVersion, WindowStart: start, WindowEnd: end,
			Result: Result{Score: score, Confidence: 0.95, Contributions: map[string]float64{"motor_temperature": score, "hydraulic_pressure": score / 2},
				SoftSensors: map[string]float64{"estimated_bearing_wear": 12.5}}}
		events, sensors, err := Publications(bundle.Manifest, out, end.Add(50*time.Millisecond), pressPrefix)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || len(sensors) != 1 || sensors[0].Unit != "%" || sensors[0].Value != 12.5 {
			t.Fatal(events, sensors)
		}
		event, err := v.Validate("inference-event", events[0].Payload)
		if err != nil {
			t.Fatalf("score %.2f: published event fails the contract: %v\n%s", score, err, events[0].Payload)
		}
		if err := reg.ValidateInferenceEventTopic(events[0].Topic, event); err != nil {
			t.Fatalf("score %.2f: %v", score, err)
		}
		want := map[bool]string{true: "warning", false: "info"}[score >= 0.7]
		if score >= 0.9 {
			want = "critical"
		}
		if event["severity"] != want || event["model_version"] != "1.2.0" {
			t.Fatalf("score %.2f: severity %v, want %s", score, event["severity"], want)
		}
	}
}

func TestPublicationsSkipMissingOrNonFiniteSoftSensor(t *testing.T) {
	key := testKey(0)
	bundle, err := LoadBundle(writeBundle(t, key, []byte("x"), nil), testValidator(t), trustFor(key))
	if err != nil {
		t.Fatal(err)
	}
	start := time.UnixMilli(1792152000000)
	out := &Output{ModelID: "m", ModelVersion: "1.2.0", WindowStart: start, WindowEnd: start.Add(time.Minute),
		Result: Result{Score: 0.1, Contributions: map[string]float64{"motor_temperature": 0.1}, SoftSensors: map[string]float64{}}}
	if _, sensors, _ := Publications(bundle.Manifest, out, start.Add(2*time.Minute), pressPrefix); len(sensors) != 0 {
		t.Fatal("a missing soft-sensor value must not be published", sensors)
	}
}

// TestDerivedPublicationsUseOwnTopicAndWindowTime checks soft sensors go to a
// derived subtree bound to the asset, never /telemetry, stamped at the window end.
func TestDerivedPublicationsUseOwnTopicAndWindowTime(t *testing.T) {
	reg := registry(t)
	start := time.UnixMilli(1792152000000)
	out := &Output{ModelID: "press-04-thermal", ModelVersion: "1.0.0", WindowStart: start, WindowEnd: start.Add(time.Minute), Result: Result{Confidence: 0.9}}
	pubs, err := DerivedPublications([]SoftSensorObservation{{Asset: "urn:openump:acme:berlin:press-04", Measurement: "estimated_bearing_wear", Value: 12.5, Unit: "%"}}, out, pressPrefix)
	if err != nil || len(pubs) != 1 {
		t.Fatal(pubs, err)
	}
	if !strings.HasSuffix(pubs[0].Topic, "/derived/estimated_bearing_wear") || strings.Contains(pubs[0].Topic, "/telemetry") {
		t.Fatal("soft sensors must use the derived subtree, never telemetry:", pubs[0].Topic)
	}
	if err := reg.ValidateDerivedTopic(pubs[0].Topic, "urn:openump:acme:berlin:press-04", "estimated_bearing_wear"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(pubs[0].Payload, &got)
	if got["observed_at"] != "2026-10-16T12:01:00.000Z" || got["model_version"] != "1.0.0" || got["value"] != 12.5 {
		t.Fatal("derived values describe their window and model:", string(pubs[0].Payload))
	}
	if err := reg.ValidateDerivedTopic(pubs[0].Topic, "urn:openump:acme:berlin:press-04", "other_measurement"); err == nil {
		t.Fatal("topic must bind to the published measurement")
	}
}

func TestEventsWithoutContributorsAreRefused(t *testing.T) {
	key := testKey(0)
	bundle, err := LoadBundle(writeBundle(t, key, []byte("x"), nil), testValidator(t), trustFor(key))
	if err != nil {
		t.Fatal(err)
	}
	start := time.UnixMilli(1792152000000)
	out := &Output{ModelID: "m", ModelVersion: "1.2.0", WindowStart: start, WindowEnd: start.Add(time.Minute), Result: Result{Score: 0.95}}
	if _, _, err := Publications(bundle.Manifest, out, start.Add(2*time.Minute), pressPrefix); err == nil {
		t.Fatal("an event with no contributing measurement must not be built")
	}
}

func TestCheckerRejectsInvalidOutgoingPublications(t *testing.T) {
	c := Checker{Validator: testValidator(t), Registry: registry(t)}
	if err := c.Event(Publication{Topic: "openump/v1/acme/berlin/assembly/line-01/press-04/event/x", Payload: []byte(`{"schema_version":"1.0"}`)}); err == nil {
		t.Fatal("a schema-invalid event must be refused before publishing")
	}
	bad := Publication{Topic: "openump/v1/acme/berlin/assembly/line-01/press-04/derived/other", Payload: []byte(`{"asset":"urn:openump:acme:berlin:press-04","measurement":"estimated_bearing_wear","value":1}`)}
	if err := c.Derived(bad); err == nil {
		t.Fatal("a derived value on the wrong topic must be refused")
	}
}

package contracts

import (
	"strconv"
	"strings"
	"time"
)

// semverLess reports whether a sorts before b; both match the schema pattern.
func semverLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// manifestSemantics checks Edge AI model-manifest rules a schema cannot express.
// It does not verify the signature or artifact digest; hades-infer does that
// against the artifact bytes and a trusted keyring (Hades-036).
func manifestSemantics(manifest map[string]any) error {
	budgets := manifest["budgets"].(map[string]any)
	if rollback, ok := budgets["rollback_to_version"].(string); ok && !semverLess(rollback, manifest["model_version"].(string)) {
		return fail("context", "rollback target is not an earlier model version")
	}
	inputs := manifest["inputs"].(map[string]any)
	enterprise := ""
	subscribed := map[string]bool{}
	for _, raw := range inputs["bindings"].([]any) {
		binding := raw.(map[string]any)
		asset := binding["asset"].(string)
		owner := strings.Split(asset, ":")[2]
		if enterprise != "" && owner != enterprise {
			return fail("context", "input bindings span enterprises")
		}
		enterprise = owner
		key := asset + "\x00" + binding["measurement"].(string)
		if subscribed[key] {
			return fail("context", "duplicate input measurement binding")
		}
		subscribed[key] = true
	}
	events := map[string]bool{}
	sensors := map[string]bool{}
	for _, raw := range manifest["outputs"].([]any) {
		output := raw.(map[string]any)
		asset := output["asset"].(string)
		if strings.Split(asset, ":")[2] != enterprise {
			return fail("context", "output binding outside the input enterprise")
		}
		switch output["kind"] {
		case "telemetry":
			// A soft sensor must not overwrite a measurement it reads, and two
			// outputs must not write one series.
			key := asset + "\x00" + output["measurement"].(string)
			if subscribed[key] {
				return fail("context", "soft-sensor output shadows an input measurement")
			}
			if sensors[key] {
				return fail("context", "duplicate soft-sensor output")
			}
			sensors[key] = true
		case "inference-event":
			key := asset + "\x00" + output["inference_type"].(string)
			if events[key] {
				return fail("context", "duplicate inference-event output")
			}
			events[key] = true
			severity := output["severity"].(map[string]any)
			if number(severity["warning_score"]) > number(severity["critical_score"]) {
				return fail("context", "warning score exceeds critical score")
			}
		}
	}
	return nil
}

// inferenceEventSemantics checks event rules a schema cannot express.
func inferenceEventSemantics(event map[string]any) error {
	if number(event["window_end_ms"]) <= number(event["window_start_ms"]) {
		return fail("context", "empty or inverted inference window")
	}
	// The event is emitted when its window closes, never before.
	stamp, err := time.Parse("2006-01-02T15:04:05.000Z", event["timestamp"].(string))
	if err != nil || float64(stamp.UnixMilli()) < number(event["window_end_ms"]) {
		return fail("context", "event timestamp precedes the end of its inference window")
	}
	score, threshold := number(event["anomaly_score"]), number(event["threshold"])
	if alerting := event["severity"] != "info"; alerting != (score >= threshold) {
		return fail("context", "severity disagrees with the score and threshold")
	}
	seen := map[string]bool{}
	for _, raw := range event["top_contributors"].([]any) {
		measurement := raw.(map[string]any)["measurement"].(string)
		if seen[measurement] {
			return fail("context", "duplicate contributing measurement")
		}
		seen[measurement] = true
	}
	return nil
}

// derivedValueSemantics checks soft-sensor rules a schema cannot express:
// a non-empty window and an observation time equal to the window end, so a
// value always describes the data it was computed from.
func derivedValueSemantics(value map[string]any) error {
	if number(value["window_end_ms"]) <= number(value["window_start_ms"]) {
		return fail("context", "empty or inverted derived-value window")
	}
	stamp, err := time.Parse("2006-01-02T15:04:05.000Z", value["observed_at"].(string))
	if err != nil || float64(stamp.UnixMilli()) != number(value["window_end_ms"]) {
		return fail("context", "derived observed_at is not the end of its window")
	}
	return nil
}

// SoftSensor is one soft-sensor output a verified model manifest declares.
type SoftSensor struct {
	ModelID, ModelVersion, Asset, Measurement, Unit string
}

// DerivedDeclarations indexes the soft-sensor outputs of verified manifests
// by model_id@model_version and asset/measurement.
type DerivedDeclarations map[string]SoftSensor

func derivedKey(modelID, modelVersion, asset, measurement string) string {
	return modelID + "@" + modelVersion + "\x00" + asset + "\x00" + measurement
}

// Add records a declared soft-sensor output.
func (d DerivedDeclarations) Add(s SoftSensor) {
	d[derivedKey(s.ModelID, s.ModelVersion, s.Asset, s.Measurement)] = s
}

// CheckDeclarationsAgainstDevices refuses soft-sensor outputs named like one
// of their asset's device measurements. Run when a bundle is loaded, so a
// misnamed output is rejected up front rather than failing every window.
// devices maps an asset to its metadata measurements.
func (d DerivedDeclarations) CheckDeclarationsAgainstDevices(devices func(asset string) map[string]any) error {
	for _, s := range d {
		if _, device := devices(s.Asset)[s.Measurement]; device {
			return fail("context", "soft-sensor output %s reuses a device measurement name of %s", s.Measurement, s.Asset)
		}
	}
	return nil
}

// ValidateDerivedDeclaration binds a schema-valid derived value to its
// producing model: a verified manifest for model_id@model_version must declare
// a soft-sensor output for this asset and measurement with the same unit. The
// name must also not be one of the asset's device measurements, so a derived
// series can never be read as physical telemetry.
func ValidateDerivedDeclaration(value map[string]any, declared DerivedDeclarations, deviceMeasurements map[string]any) error {
	measurement, _ := value["measurement"].(string)
	if _, device := deviceMeasurements[measurement]; device {
		return fail("context", "derived value claims a device measurement")
	}
	modelID, _ := value["model_id"].(string)
	version, _ := value["model_version"].(string)
	asset, _ := value["asset"].(string)
	d, ok := declared[derivedKey(modelID, version, asset, measurement)]
	if !ok {
		return fail("context", "measurement is not a declared soft-sensor output of a verified model")
	}
	if d.Unit != value["unit"] {
		return fail("context", "derived unit differs from the model declaration")
	}
	return nil
}

// ValidateInferenceEventTopic binds an event to
// openump/v1/<enterprise>/<site>/<area>/<line>/<asset>/event/<inference_type>,
// rechecking the asset against the registry through its telemetry topic.
func (r *Registry) ValidateInferenceEventTopic(topic string, event map[string]any) error {
	inferenceType, _ := event["inference_type"].(string)
	asset, _ := event["asset"].(string)
	return r.validateSuffixedTopic(topic, asset, "event", inferenceType, "invalid inference-event publication topic")
}

// ValidateDerivedTopic binds a soft-sensor publication to
// openump/v1/<enterprise>/<site>/<area>/<line>/<asset>/derived/<measurement>.
// Derived values live on their own subtree so they are never read as device
// telemetry, and so actuation preconditions cannot consume them.
func (r *Registry) ValidateDerivedTopic(topic, asset, measurement string) error {
	return r.validateSuffixedTopic(topic, asset, "derived", measurement, "invalid derived publication topic")
}

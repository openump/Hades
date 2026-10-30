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

// ValidateInferenceEventTopic binds an event to
// openump/v1/<enterprise>/<site>/<area>/<line>/<asset>/event/<inference_type>,
// rechecking the asset against the registry through its telemetry topic.
func (r *Registry) ValidateInferenceEventTopic(topic string, event map[string]any) error {
	inferenceType, _ := event["inference_type"].(string)
	asset, _ := event["asset"].(string)
	return r.validateSuffixedTopic(topic, asset, "event", inferenceType, "invalid inference-event publication topic")
}

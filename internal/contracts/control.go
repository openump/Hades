package contracts

import (
	"strings"
	"time"
)

func policySemantics(policy map[string]any) error {
	seen := map[string]bool{}
	for _, item := range policy["assets"].([]any) {
		asset := item.(map[string]any)
		id := asset["asset"].(string)
		if seen[id] || strings.Split(id, ":")[2] != policy["enterprise"] {
			return fail("context", "duplicate asset or enterprise mismatch")
		}
		seen[id] = true
		if asset["autonomy_level"].(string) > policy["site_autonomy_level"].(string) {
			return fail("context", "asset autonomy exceeds site ceiling")
		}
		for _, item := range asset["parameters"].(map[string]any) {
			parameter := item.(map[string]any)
			if number(parameter["min_value"]) > number(parameter["max_value"]) {
				return fail("context", "inverted writable envelope")
			}
			addresses := map[string]bool{}
			for _, address := range parameter["mailbox"].(map[string]any) {
				s := address.(string)
				if addresses[s] {
					return fail("context", "mailbox read/write roles overlap")
				}
				addresses[s] = true
			}
		}
	}
	return nil
}
func receiptSemantics(receipt map[string]any) error {
	for i, item := range receipt["gates"].([]any) {
		gate := item.(map[string]any)
		if number(gate["gate"]) != float64(i+1) {
			return fail("context", "gate audit must list gates 1..5 exactly once in order")
		}
		if receipt["state"] == "EXECUTED" && gate["result"] != "PASS" {
			return fail("context", "EXECUTED requires five passed gates")
		}
	}
	return nil
}

// ValidateCommandContext checks an offline proposal against approved policy and
// namespace at an explicit time. It does NOT verify signatures, live interlocks,
// operator confirmation, idempotency, cooldown, or authorize/perform PLC writes.
func (v *Validator) ValidateCommandContext(raw, policyRaw []byte, topic string, registry *Registry, now time.Time) (map[string]any, error) {
	command, err := v.Validate("command", raw)
	if err != nil {
		return nil, err
	}
	policy, err := v.Validate("actuation-policy", policyRaw)
	if err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, fail("context", "approved asset registry required")
	}
	if err = registry.ValidateControlTopic(topic, command["asset"].(string), command["target_parameter"].(string), "command"); err != nil {
		return nil, err
	}
	parts := strings.Split(topic, "/")
	if parts[2] != policy["enterprise"] || parts[3] != policy["site"] || number(command["policy_revision"]) != number(policy["policy_revision"]) {
		return nil, fail("context", "policy scope/revision mismatch")
	}
	issued := int64(number(command["issued_at_ms"]))
	ttl := int64(number(command["ttl_ms"]))
	if now.UnixMilli() < issued || now.UnixMilli()-issued >= ttl {
		return nil, fail("context", "expired or future-issued command")
	}
	origin := command["origin"].(map[string]any)
	for _, item := range policy["assets"].([]any) {
		asset := item.(map[string]any)
		if asset["asset"] != command["asset"] {
			continue
		}
		if origin["autonomy_level"].(string) > asset["autonomy_level"].(string) {
			return nil, fail("context", "autonomy exceeds asset ceiling")
		}
		allowed := false
		for _, item := range asset["allowed_origins"].([]any) {
			entry := item.(map[string]any)
			if entry["actor_id"] == origin["actor_id"] && entry["key_id"] == origin["signature"].(map[string]any)["key_id"] {
				allowed = true
			}
		}
		if !allowed {
			return nil, fail("context", "origin/key not allowlisted")
		}
		item, ok := asset["parameters"].(map[string]any)[command["target_parameter"].(string)]
		if !ok {
			return nil, fail("context", "parameter not allowlisted")
		}
		parameter := item.(map[string]any)
		value := number(command["target_value"])
		if command["unit"] != parameter["unit"] || value < number(parameter["min_value"]) || value > number(parameter["max_value"]) {
			return nil, fail("context", "unit or safety envelope mismatch")
		}
		for _, required := range parameter["required_preconditions"].([]any) {
			found := false
			for _, supplied := range command["preconditions"].([]any) {
				if samePrecondition(required.(map[string]any), supplied.(map[string]any)) {
					found = true
				}
			}
			if !found {
				return nil, fail("context", "required policy precondition omitted or changed")
			}
		}
		return command, nil
	}
	return nil, fail("context", "asset not allowlisted")
}

// ValidateControlTopic keeps control topics separate from telemetry validation.
func (r *Registry) ValidateControlTopic(topic, asset, target, kind string) error {
	parts := strings.Split(topic, "/")
	if len(parts) != 9 || len(topic) > 512 || (kind != "command" && kind != "command_receipt") || parts[7] != kind || !controlParameter(target) || parts[8] != target {
		return fail("namespace", "invalid control publication topic")
	}
	return r.ValidateTopic(strings.Join(parts[:7], "/")+"/telemetry", asset)
}
func controlParameter(s string) bool { // Same bounded identifier as measurement_name.
	if len(s) < 1 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func samePrecondition(a, b map[string]any) bool {
	return a["measurement"] == b["measurement"] && a["operator"] == b["operator"] && a["unit"] == b["unit"] && number(a["value"]) == number(b["value"]) && number(a["max_age_ms"]) == number(b["max_age_ms"])
}

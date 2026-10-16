package contracts

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestControlFixtures(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/contracts/control/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Kind, Error string
		Payload           json.RawMessage
		Valid             bool
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := v.Validate(c.Kind, c.Payload)
			if (err == nil) != c.Valid {
				t.Fatal(err, c.Valid)
			}
		})
	}
}
func TestCommandPolicyContext(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	command, err := os.ReadFile("../../examples/contracts/control/command.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile("../../examples/contracts/control/actuation-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/contracts/assets.json")
	if err != nil {
		t.Fatal(err)
	}
	var assets []Asset
	if err = json.Unmarshal(raw, &assets); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(assets)
	if err != nil {
		t.Fatal(err)
	}
	topic := "openump/v1/acme/berlin/assembly/line-01/press-04/command/pressure_setpoint"
	stamp := time.UnixMilli(1792152000001)
	if _, err = v.ValidateCommandContext(command, policy, topic, registry, stamp); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"expired", "future", "envelope", "unit", "preconditions", "autonomy", "actor", "target", "revision", "site", "mailbox", "range"} {
		t.Run(change, func(t *testing.T) {
			var c, p map[string]any
			json.Unmarshal(command, &c)
			json.Unmarshal(policy, &p)
			now := stamp
			switch change {
			case "expired":
				now = now.Add(5 * time.Second)
			case "future":
				now = now.Add(-time.Second)
			case "envelope":
				c["target_value"] = 11
			case "unit":
				c["unit"] = "Pa"
			case "preconditions":
				c["preconditions"].([]any)[0].(map[string]any)["value"] = 0
			case "autonomy":
				c["origin"].(map[string]any)["autonomy_level"] = "L3"
			case "actor":
				c["origin"].(map[string]any)["actor_id"] = "stranger"
			case "target":
				c["target_parameter"] = "unapproved"
			case "revision":
				c["policy_revision"] = 2
			case "site":
				p["site"] = "other"
			case "mailbox":
				m := p["assets"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["pressure_setpoint"].(map[string]any)["mailbox"].(map[string]any)
				m["proposed_value"] = m["active_value"]
			case "range":
				p["assets"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["pressure_setpoint"].(map[string]any)["min_value"] = 20
			}
			cr, _ := json.Marshal(c)
			pr, _ := json.Marshal(p)
			if _, err := v.ValidateCommandContext(cr, pr, topic, registry, now); err == nil {
				t.Fatal("unsafe proposal accepted")
			}
		})
	}
	for _, bad := range []string{topic + "/extra", "openump/v1/acme/other/assembly/line-01/press-04/command/pressure_setpoint", "openump/v1/acme/berlin/assembly/line-01/press-04/command/+"} {
		if err = registry.ValidateControlTopic(bad, "urn:openump:acme:berlin:press-04", "pressure_setpoint", "command"); err == nil {
			t.Fatal(bad)
		}
	}
	if err = registry.ValidateTopic(topic, "urn:openump:acme:berlin:press-04"); err == nil {
		t.Fatal("telemetry namespace accepted control")
	}
}

func TestControlReceiptGateAuditAndStrictWire(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/contracts/control/command-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	r["state"] = "EXECUTED"
	r["terminal"] = true
	r["pre_write_value"] = 4
	r["post_write_value"] = 5
	for _, gate := range r["gates"].([]any) {
		gate.(map[string]any)["result"] = "PASS"
	}
	encoded, _ := json.Marshal(r)
	if _, err = v.Validate("command-receipt", encoded); err != nil {
		t.Fatal(err)
	}
	r["gates"].([]any)[4].(map[string]any)["result"] = "NOT_EVALUATED"
	encoded, _ = json.Marshal(r)
	if _, err = v.Validate("command-receipt", encoded); err == nil {
		t.Fatal("unverified execution accepted")
	}
	r["gates"].([]any)[4].(map[string]any)["result"] = "PASS"
	r["gates"].([]any)[4].(map[string]any)["gate"] = 4
	encoded, _ = json.Marshal(r)
	if _, err = v.Validate("command-receipt", encoded); err == nil {
		t.Fatal("duplicate gate accepted")
	}
	command, err := os.ReadFile("../../examples/contracts/control/command.json")
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	json.Unmarshal(command, &c)
	c["command_id"] = c["command_id"].(string) + "\n"
	encoded, _ = json.Marshal(c)
	if _, err = v.Validate("command", encoded); err == nil {
		t.Fatal("trailing UUID newline accepted")
	}
}

package mcp

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/contracts"
)

var testNow = time.UnixMilli(1792152000000)

func mcpKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte(strings.Repeat("m", ed25519.SeedSize)))
}

// policyWith returns the example actuation policy with the hades-mcp origin
// added to press-04's allowed_origins when allow is true.
func policyWith(t *testing.T, allow bool, ceiling string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../examples/contracts/control/actuation-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	json.Unmarshal(raw, &policy)
	policy["site_autonomy_level"] = ceiling
	asset := policy["assets"].([]any)[0].(map[string]any)
	asset["autonomy_level"] = ceiling
	if allow {
		asset["allowed_origins"] = append(asset["allowed_origins"].([]any), map[string]any{"actor_id": "hades-mcp", "key_id": "hades-mcp-key"})
	}
	out, _ := json.Marshal(policy)
	return out
}

func testProposer(t *testing.T, policy []byte) *Proposer {
	t.Helper()
	v, err := contracts.New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("../../examples/contracts/assets.json")
	var assets []contracts.Asset
	json.Unmarshal(raw, &assets)
	reg, err := contracts.NewRegistry(assets)
	if err != nil {
		t.Fatal(err)
	}
	return &Proposer{
		Validator: v, Registry: reg, Policy: policy,
		ActorID: "hades-mcp", KeyID: "hades-mcp-key", Key: mcpKey(), TTL: 5 * time.Second,
		TopicFor: func(asset, parameter string) (string, error) {
			return "openump/v1/acme/berlin/assembly/line-01/press-04/command/" + parameter, nil
		},
		Now:   func() time.Time { return testNow },
		NewID: func(time.Time) string { return "019a1234-5678-7000-8000-000000000340" },
	}
}

func TestProposalBuildsSignedL1CommandWithPolicyPreconditions(t *testing.T) {
	p := testProposer(t, policyWith(t, true, "L2"))
	proposal, err := p.Build(ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 5})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Autonomy != "L1" {
		t.Fatal("a staged MCP proposal must be L1, awaiting an operator:", proposal.Autonomy)
	}
	var command map[string]any
	json.Unmarshal(proposal.Command, &command)
	origin := command["origin"].(map[string]any)
	if origin["actor_id"] != "hades-mcp" || origin["autonomy_level"] != "L1" {
		t.Fatal(origin)
	}
	preconditions := command["preconditions"].([]any)
	if len(preconditions) != 1 || preconditions[0].(map[string]any)["measurement"] != "machine_state" {
		t.Fatal("preconditions must be the policy's required set, verbatim:", preconditions)
	}
	// The proposal verifies under the hades-mcp public key, as the actuator would check.
	keys := map[string]ed25519.PublicKey{"hades-mcp-key": mcpKey().Public().(ed25519.PublicKey)}
	if err := actuator.Verify(proposal.Command, keys, false); err != nil {
		t.Fatal(err)
	}
	if dry, err := p.Build(ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 5, DryRun: true}); err != nil || dry.Autonomy != "L0" {
		t.Fatal("a dry run must be L0 shadow evaluation", dry, err)
	}
}

func TestProposalRefusedOffline(t *testing.T) {
	cases := map[string]struct {
		policy []byte
		req    ProposalRequest
		want   string
	}{
		"origin not allowlisted": {policyWith(t, false, "L2"), ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 5}, "origin/key not allowlisted"},
		"above safety envelope":  {policyWith(t, true, "L2"), ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 11}, "safety envelope"},
		"below safety envelope":  {policyWith(t, true, "L2"), ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: -1}, "safety envelope"},
		"parameter not listed":   {policyWith(t, true, "L2"), ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "spindle_speed", Value: 5}, "not allowlisted"},
		"asset not in policy":    {policyWith(t, true, "L2"), ProposalRequest{Asset: "urn:openump:acme:berlin:press-05", Parameter: "pressure_setpoint", Value: 5}, "not in the actuation policy"},
		"site ceiling is L0":     {policyWith(t, true, "L0"), ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 5}, "autonomy exceeds asset ceiling"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := testProposer(t, tc.policy).Build(tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected refusal containing %q, got %v", tc.want, err)
			}
		})
	}
}

// TestAutonomyCapHoldsEvenWhenPolicyAllowsL3: hades-mcp never emits L2/L3.
func TestAutonomyCapHoldsEvenWhenPolicyAllowsL3(t *testing.T) {
	if MaxAutonomy != "L1" {
		t.Fatal("the MCP autonomy cap must be L1")
	}
	proposal, err := testProposer(t, policyWith(t, true, "L3")).Build(ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 5})
	if err != nil || proposal.Autonomy != "L1" {
		t.Fatal("even an L3 policy must not let an AI proposal exceed L1", proposal, err)
	}
}

func TestReceiptMustBeTrustedAndBoundToProposal(t *testing.T) {
	p := testProposer(t, policyWith(t, true, "L2"))
	proposal, err := p.Build(ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 5})
	if err != nil {
		t.Fatal(err)
	}
	actuatorKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("a", ed25519.SeedSize)))
	trusted := map[string]ed25519.PublicKey{"actuator-key": actuatorKey.Public().(ed25519.PublicKey)}
	receipt := func(commandID, digest string, key ed25519.PrivateKey) []byte {
		raw, _ := os.ReadFile("../../examples/contracts/control/command-receipt.json")
		var r map[string]any
		json.Unmarshal(raw, &r)
		r["command_id"], r["command_sha256"] = commandID, digest
		r["signature"].(map[string]any)["key_id"] = "actuator-key"
		unsigned, _ := json.Marshal(r)
		signed, err := actuator.Sign(unsigned, key, true)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	if _, err := VerifyReceipt(receipt(proposal.CommandID, proposal.Digest, actuatorKey), proposal, p.Validator, trusted); err != nil {
		t.Fatal("a trusted receipt for this exact command must verify:", err)
	}
	rogue := ed25519.NewKeyFromSeed([]byte(strings.Repeat("r", ed25519.SeedSize)))
	for name, raw := range map[string][]byte{
		"untrusted signer": receipt(proposal.CommandID, proposal.Digest, rogue),
		"other command id": receipt("019a1234-5678-7000-8000-000000000999", proposal.Digest, actuatorKey),
		"other digest":     receipt(proposal.CommandID, strings.Repeat("0", 64), actuatorKey),
	} {
		if _, err := VerifyReceipt(raw, proposal, p.Validator, trusted); err == nil {
			t.Fatalf("%s: receipt must be rejected", name)
		}
	}
}

// Package mcp is a safety-gated Industrial Model Context Protocol server. It
// exposes read-only plant context and a propose_command tool whose only write
// path is a signed proposal to hades-actuator's five-gate engine. It contains
// no fieldbus or PLC code.
package mcp

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/contracts"
)

// MaxAutonomy is the highest autonomy an AI-originated proposal may request.
// L0 is shadow evaluation; L1 queues the proposal for explicit operator
// confirmation. hades-mcp never emits L2/L3, so an AI agent can never cause an
// unconfirmed PLC write, whatever the site actuation policy allows.
const MaxAutonomy = "L1"

// allowedAutonomy is the explicit set of levels hades-mcp may emit.
var allowedAutonomy = map[string]bool{"L0": true, "L1": true}

// ProposalRequest is what an agent may choose. Everything else in the command
// (identity, origin, TTL, policy revision, preconditions) is set by hades-mcp
// from the reviewed policy, so an agent cannot weaken a gate.
type ProposalRequest struct {
	Asset     string  `json:"asset"`
	Parameter string  `json:"parameter"`
	Value     float64 `json:"value"`
	DryRun    bool    `json:"dry_run" jsonschema:"required: true for shadow evaluation (L0), false to queue for operator confirmation (L1)"`
}

// Proposer builds, checks and signs commands as the distinct hades-mcp origin.
type Proposer struct {
	Validator *contracts.Validator
	Registry  *contracts.Registry
	Policy    []byte // reviewed actuation policy (raw JSON)
	ActorID   string // hades-mcp origin actor, allowlisted per asset by the site
	KeyID     string
	Key       ed25519.PrivateKey
	TTL       time.Duration
	TopicFor  func(asset, parameter string) (string, error)
	Now       func() time.Time
	NewID     func(time.Time) string

	once   sync.Once
	parsed map[string]any
	err    error
}

// Proposal is a signed, offline-validated command ready for the actuator.
type Proposal struct {
	CommandID string
	Topic     string
	Command   []byte // canonical signed command
	Digest    string // SHA-256 of the canonical signed command (receipt binding)
	Autonomy  string
}

// Build turns an agent request into a signed command, refusing it offline if
// it breaks the schema, the actuation policy or the autonomy cap. Nothing is
// sent unless Build succeeds.
func (p *Proposer) Build(req ProposalRequest) (*Proposal, error) {
	autonomy := "L1"
	if req.DryRun {
		autonomy = "L0"
	}
	if !allowedAutonomy[autonomy] {
		return nil, fmt.Errorf("autonomy %s exceeds the MCP cap %s", autonomy, MaxAutonomy)
	}
	parameter, revision, err := p.policyParameter(req.Asset, req.Parameter)
	if err != nil {
		return nil, err
	}
	topic, err := p.TopicFor(req.Asset, req.Parameter)
	if err != nil {
		return nil, err
	}
	now := p.Now()
	command := map[string]any{
		"schema_version":   "1.0",
		"command_id":       p.NewID(now),
		"issued_at_ms":     now.UnixMilli(),
		"ttl_ms":           p.TTL.Milliseconds(),
		"policy_revision":  revision,
		"origin":           map[string]any{"actor_id": p.ActorID, "autonomy_level": autonomy, "signature": map[string]any{"algorithm": "Ed25519", "canonicalization": "RFC8785", "key_id": p.KeyID, "value": ""}},
		"asset":            req.Asset,
		"target_parameter": req.Parameter,
		"target_value":     req.Value,
		"unit":             parameter["unit"],
		// The policy's required preconditions, verbatim: an agent never chooses them.
		"preconditions": parameter["required_preconditions"],
	}
	unsigned, err := json.Marshal(command)
	if err != nil {
		return nil, err
	}
	signed, err := actuator.Sign(unsigned, p.Key, false)
	if err != nil {
		return nil, err
	}
	// The same offline checks the actuator applies: schema, policy scope and
	// revision, TTL, autonomy ceiling, origin allowlist, parameter allowlist,
	// unit, safety envelope and required preconditions.
	if _, err := p.Validator.ValidateCommandContext(signed, p.Policy, topic, p.Registry, now); err != nil {
		return nil, fmt.Errorf("proposal refused offline: %w", err)
	}
	// actuator.Canonical is exactly what the actuator hashes into the receipt's
	// command_sha256, so the receipt binding cannot drift.
	canonical, err := actuator.Canonical(signed)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return &Proposal{CommandID: command["command_id"].(string), Topic: topic, Command: canonical, Digest: hex.EncodeToString(sum[:]), Autonomy: autonomy}, nil
}

// policyParameter returns the allowlisted parameter definition and the policy
// revision, refusing assets and parameters the policy does not list.
func (p *Proposer) policyParameter(asset, name string) (map[string]any, any, error) {
	// The reviewed policy is parsed once; ValidateCommandContext still checks the
	// exact policy bytes on every proposal, as the actuator will.
	p.once.Do(func() { p.parsed, p.err = p.Validator.Validate("actuation-policy", p.Policy) })
	if p.err != nil {
		return nil, nil, p.err
	}
	policy := p.parsed
	for _, item := range policy["assets"].([]any) {
		entry := item.(map[string]any)
		if entry["asset"] != asset {
			continue
		}
		parameter, ok := entry["parameters"].(map[string]any)[name].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("parameter %q is not allowlisted for %s", name, asset)
		}
		return parameter, policy["policy_revision"], nil
	}
	return nil, nil, fmt.Errorf("asset %s is not in the actuation policy", asset)
}

// VerifyReceipt checks that a receipt is signed by a trusted actuator key and
// answers exactly this proposal: same command ID and same signed-command digest.
func VerifyReceipt(raw []byte, proposal *Proposal, validator *contracts.Validator, actuatorKeys map[string]ed25519.PublicKey) (map[string]any, error) {
	receipt, err := validator.Validate("command-receipt", raw)
	if err != nil {
		return nil, err
	}
	if err := actuator.Verify(raw, actuatorKeys, true); err != nil {
		return nil, fmt.Errorf("untrusted receipt: %w", err)
	}
	if receipt["command_id"] != proposal.CommandID || receipt["command_sha256"] != proposal.Digest {
		return nil, fmt.Errorf("receipt does not answer this proposal")
	}
	return receipt, nil
}

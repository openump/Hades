package actuator

import (
	"encoding/json"
	"fmt"
)

// recoverUnfinished never repeats an uncertain mailbox write after a crash.
// The command stays consumed and a signed TIMEOUT makes ambiguity explicit.
func (e *Engine) recoverUnfinished() error {
	for id, own := range e.owned {
		if len(own.Receipt) > 0 {
			continue
		}
		var c Command
		if err := json.Unmarshal(own.intent.Command, &c); err != nil {
			return err
		}
		gates := own.intent.Gates
		if len(gates) != 5 {
			return fmt.Errorf("incomplete recovered gate snapshot")
		}
		receipt := map[string]any{"schema_version": "1.0", "command_id": id, "command_sha256": own.intent.Digest, "asset": c.Asset, "target_parameter": c.Parameter, "policy_revision": c.Revision, "recorded_at_ms": e.opts.Now().UnixMilli(), "state": "TIMEOUT", "terminal": true, "pre_write_value": nil, "post_write_value": nil, "unit": c.Unit, "gates": gates, "signature": map[string]any{"algorithm": "Ed25519", "canonicalization": "RFC8785", "key_id": e.opts.SigningKeyID, "value": ""}}
		raw, _ := json.Marshal(receipt)
		signed, err := Sign(raw, e.opts.SigningKey, true)
		if err != nil {
			return err
		}
		if _, err = e.opts.Config.Validator.Validate("command-receipt", signed); err != nil {
			return err
		}
		if err = e.audit.Append("receipt", json.RawMessage(signed)); err != nil {
			return err
		}
		own.Receipt = signed
		own.Pending = false
	}
	return nil
}

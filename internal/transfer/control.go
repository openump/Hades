package transfer

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/sample"
)

// ReceiptMessageID gives each immutable signed receipt (pending and terminal)
// its own stable transfer identity without changing signed payload bytes.
func ReceiptMessageID(raw []byte) string {
	h := sha256.Sum256(raw)
	h[6] = (h[6] & 15) | 0x70
	h[8] = (h[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func ValidateWithReceiptKeys(cfg *sample.Config, topic string, raw []byte, keys map[string]ed25519.PublicKey) (map[string]any, error) {
	if !strings.Contains(topic, "/command_receipt/") {
		return Validate(cfg, topic, raw)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("command receipt trust not configured")
	}
	p, err := cfg.Validator.Validate("command-receipt", raw)
	if err != nil {
		return nil, err
	}
	if err = cfg.Registry.ValidateControlTopic(topic, p["asset"].(string), p["target_parameter"].(string), "command_receipt"); err != nil {
		return nil, err
	}
	if err = actuator.Verify(raw, keys, true); err != nil {
		return nil, err
	}
	// Only transport bookkeeping receives this ID, never the signed body.
	out := map[string]any{"message_id": ReceiptMessageID(raw)}
	return out, nil
}

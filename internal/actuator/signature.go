// Package actuator mediates signed proposals; it is not a safety PLC.
package actuator

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/openump/Hades/internal/contracts"
)

func Canonical(raw []byte) ([]byte, error) {
	if _, err := contracts.Decode(raw); err != nil {
		return nil, err
	}
	return jsoncanonicalizer.Transform(raw)
}
func signing(raw []byte, receipt bool) ([]byte, error) {
	value, err := contracts.Decode(raw)
	if err != nil {
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object")
	}
	parent := obj
	domain := "OpenUMP command-receipt v1\n"
	if !receipt {
		parent, ok = obj["origin"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing origin")
		}
		domain = "OpenUMP command v1\n"
	}
	sig, ok := parent["signature"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing signature")
	}
	delete(sig, "value")
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	jcs, err := Canonical(encoded)
	return append([]byte(domain), jcs...), err
}
func Sign(raw []byte, key ed25519.PrivateKey, receipt bool) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid signing key")
	}
	data, err := signing(raw, receipt)
	if err != nil {
		return nil, err
	}
	value, _ := contracts.Decode(raw)
	obj := value.(map[string]any)
	parent := obj
	if !receipt {
		parent = obj["origin"].(map[string]any)
	}
	parent["signature"].(map[string]any)["value"] = base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return Canonical(encoded)
}
func Verify(raw []byte, keys map[string]ed25519.PublicKey, receipt bool) error {
	value, err := contracts.Decode(raw)
	if err != nil {
		return err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("expected object")
	}
	parent := obj
	if !receipt {
		parent, ok = obj["origin"].(map[string]any)
		if !ok {
			return fmt.Errorf("missing origin")
		}
	}
	sig, ok := parent["signature"].(map[string]any)
	if !ok || sig["algorithm"] != "Ed25519" || sig["canonicalization"] != "RFC8785" {
		return fmt.Errorf("unsupported signature")
	}
	id, _ := sig["key_id"].(string)
	encoded, _ := sig["value"].(string)
	signature, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return err
	}
	key := keys[id]
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("unknown signing key")
	}
	data, err := signing(raw, receipt)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, data, signature) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// Package infer runs signed, budgeted Edge AI models next to the edge stack.
// It never writes to a controller and has no access to the command plane.
package infer

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/openump/Hades/internal/contracts"
)

// manifestDomain separates model-manifest signatures from command and receipt
// signatures, so no valid signature over one contract verifies as another.
const manifestDomain = "OpenUMP model-manifest v1\n"

const (
	ManifestFile     = "model-manifest.json"
	ArtifactFile     = "model.artifact"
	maxManifestBytes = contracts.MaxPayloadBytes
	maxArtifactBytes = 64 << 20
)

// Bundle is a verified model package: a manifest whose Ed25519 signature and
// artifact digest both checked out against the site trust store.
type Bundle struct {
	Manifest Manifest
	Artifact []byte
	Dir      string
}

// Manifest is the decoded, schema-valid model manifest.
type Manifest struct {
	ModelID      string `json:"model_id"`
	ModelVersion string `json:"model_version"`
	RuntimeType  string `json:"runtime_type"`
	Signature    struct {
		SignerID string `json:"signer_id"`
		KeyID    string `json:"key_id"`
	} `json:"signature"`
	Inputs struct {
		Bindings []struct {
			Asset       string `json:"asset"`
			Measurement string `json:"measurement"`
			Unit        string `json:"unit"`
		} `json:"bindings"`
		WindowSizeSamples int     `json:"window_size_samples"`
		WindowDurationMS  int64   `json:"window_duration_ms"`
		MinGoodRatio      float64 `json:"min_good_ratio"`
	} `json:"inputs"`
	Outputs []struct {
		Kind          string `json:"kind"`
		Asset         string `json:"asset"`
		Measurement   string `json:"measurement"`
		Unit          string `json:"unit"`
		InferenceType string `json:"inference_type"`
		Severity      struct {
			WarningScore  float64 `json:"warning_score"`
			CriticalScore float64 `json:"critical_score"`
		} `json:"severity"`
	} `json:"outputs"`
	Budgets struct {
		MaxMemoryMB            int     `json:"max_memory_mb"`
		MaxInferenceLatencyMS  int     `json:"max_inference_latency_ms"`
		MaxConsecutiveFailures int     `json:"max_consecutive_failures"`
		RollbackToVersion      *string `json:"rollback_to_version"`
	} `json:"budgets"`
}

// Trust maps signing key IDs to public keys, provisioned by the operator.
type Trust map[string]ed25519.PublicKey

// LoadBundle verifies a model package before anything can activate it. In
// order: manifest schema and semantics, Ed25519 signature over the RFC 8785
// canonical manifest, then the artifact SHA-256. Any failure rejects the
// whole package.
func LoadBundle(dir string, validator *contracts.Validator, trust Trust) (*Bundle, error) {
	raw, err := readBounded(filepath.Join(dir, ManifestFile), maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("bundle %s: manifest: %w", dir, err)
	}
	if _, err := validator.Validate("model-manifest", raw); err != nil {
		return nil, fmt.Errorf("bundle %s: manifest invalid: %w", dir, err)
	}
	if err := VerifyManifest(raw, trust); err != nil {
		return nil, fmt.Errorf("bundle %s: %w", dir, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("bundle %s: manifest decode: %w", dir, err)
	}
	artifact, err := readBounded(filepath.Join(dir, ArtifactFile), maxArtifactBytes)
	if err != nil {
		return nil, fmt.Errorf("bundle %s: artifact: %w", dir, err)
	}
	var declared struct {
		Digest string `json:"artifact_sha256"`
	}
	json.Unmarshal(raw, &declared)
	sum := sha256.Sum256(artifact)
	if hex.EncodeToString(sum[:]) != declared.Digest {
		return nil, fmt.Errorf("bundle %s: artifact digest mismatch", dir)
	}
	return &Bundle{Manifest: manifest, Artifact: artifact, Dir: dir}, nil
}

// VerifyManifest checks the manifest signature against the trust store.
func VerifyManifest(raw []byte, trust Trust) error {
	message, signature, keyID, err := signingInput(raw)
	if err != nil {
		return err
	}
	key := trust[keyID]
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("manifest signed by untrusted key %q", keyID)
	}
	if !ed25519.Verify(key, message, signature) {
		return fmt.Errorf("invalid manifest signature")
	}
	return nil
}

// SignManifest sets the signature value of a manifest (release tooling and
// tests). The signature covers the canonical manifest without its value.
func SignManifest(raw []byte, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid signing key")
	}
	value, err := contracts.Decode(raw)
	if err != nil {
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object")
	}
	sig, ok := obj["signature"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing signature")
	}
	sig["value"] = ""
	message, err := canonicalWithoutValue(obj)
	if err != nil {
		return nil, err
	}
	sig["value"] = base64.StdEncoding.EncodeToString(ed25519.Sign(key, message))
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return jsoncanonicalizer.Transform(encoded)
}

func signingInput(raw []byte) (message, signature []byte, keyID string, err error) {
	value, err := contracts.Decode(raw)
	if err != nil {
		return nil, nil, "", err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, nil, "", fmt.Errorf("expected object")
	}
	sig, ok := obj["signature"].(map[string]any)
	if !ok || sig["algorithm"] != "Ed25519" || sig["canonicalization"] != "RFC8785" {
		return nil, nil, "", fmt.Errorf("unsigned or unsupported manifest signature")
	}
	keyID, _ = sig["key_id"].(string)
	encoded, _ := sig["value"].(string)
	signature, err = base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, nil, "", fmt.Errorf("malformed manifest signature")
	}
	message, err = canonicalWithoutValue(obj)
	return message, signature, keyID, err
}

func canonicalWithoutValue(obj map[string]any) ([]byte, error) {
	sig := obj["signature"].(map[string]any)
	value := sig["value"]
	delete(sig, "value")
	defer func() { sig["value"] = value }()
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	jcs, err := jsoncanonicalizer.Transform(encoded)
	if err != nil {
		return nil, err
	}
	return append([]byte(manifestDomain), jcs...), nil
}

// readBounded reads a regular file no larger than limit, refusing symlinks.
func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit+1))
}

func encodeSignature(signature []byte) string {
	return base64.StdEncoding.EncodeToString(signature)
}

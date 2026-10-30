package infer

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openump/Hades/internal/contracts"
)

func testValidator(t *testing.T) *contracts.Validator {
	t.Helper()
	v, err := contracts.New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func testKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte(strings.Repeat(string(rune('a'+seed)), ed25519.SeedSize)))
}

// writeBundle writes a signed bundle for artifact, letting edit adjust the
// manifest before signing.
func writeBundle(t *testing.T, key ed25519.PrivateKey, artifact []byte, edit func(map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile("../../examples/contracts/inference/model-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	json.Unmarshal(raw, &manifest)
	sum := sha256.Sum256(artifact)
	manifest["artifact_sha256"] = hex.EncodeToString(sum[:])
	if edit != nil {
		edit(manifest)
	}
	encoded, _ := json.Marshal(manifest)
	signed, err := SignManifest(encoded, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ManifestFile), signed, 0o600)
	os.WriteFile(filepath.Join(dir, ArtifactFile), artifact, 0o600)
	return dir
}

func trustFor(key ed25519.PrivateKey) Trust {
	return Trust{"ml-release-2026": key.Public().(ed25519.PublicKey)}
}

func TestSignedBundleLoads(t *testing.T) {
	key := testKey(0)
	dir := writeBundle(t, key, []byte(`{"detector":"zscore"}`), nil)
	bundle, err := LoadBundle(dir, testValidator(t), trustFor(key))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.ModelID != "press-04-vibration-iforest" || bundle.Manifest.ModelVersion != "1.2.0" {
		t.Fatal(bundle.Manifest)
	}
}

func TestTamperedOrUntrustedBundlesAreRejectedBeforeLoad(t *testing.T) {
	key := testKey(0)
	validator := testValidator(t)
	artifact := []byte(`{"detector":"zscore"}`)
	cases := map[string]struct {
		prepare func(t *testing.T) string
		trust   Trust
		want    string
	}{
		"tampered artifact": {func(t *testing.T) string {
			dir := writeBundle(t, key, artifact, nil)
			os.WriteFile(filepath.Join(dir, ArtifactFile), []byte(`{"detector":"backdoor"}`), 0o600)
			return dir
		}, trustFor(key), "artifact digest mismatch"},
		"tampered manifest after signing": {func(t *testing.T) string {
			dir := writeBundle(t, key, artifact, nil)
			raw, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
			os.WriteFile(filepath.Join(dir, ManifestFile), []byte(strings.Replace(string(raw), `"max_memory_mb":64`, `"max_memory_mb":250`, 1)), 0o600)
			return dir
		}, trustFor(key), "invalid manifest signature"},
		"signed by an unknown key id": {func(t *testing.T) string {
			return writeBundle(t, testKey(1), artifact, func(m map[string]any) {
				m["signature"].(map[string]any)["key_id"] = "rogue-key"
			})
		}, trustFor(key), "untrusted key"},
		"trusted key id claimed by another key": {func(t *testing.T) string {
			return writeBundle(t, testKey(1), artifact, nil)
		}, trustFor(key), "invalid manifest signature"},
		"unsigned manifest": {func(t *testing.T) string {
			dir := writeBundle(t, key, artifact, nil)
			raw, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
			var m map[string]any
			json.Unmarshal(raw, &m)
			delete(m, "signature")
			encoded, _ := json.Marshal(m)
			os.WriteFile(filepath.Join(dir, ManifestFile), encoded, 0o600)
			return dir
		}, trustFor(key), "manifest invalid"},
		"artifact is a symlink": {func(t *testing.T) string {
			dir := writeBundle(t, key, artifact, nil)
			target := filepath.Join(t.TempDir(), "elsewhere")
			os.WriteFile(target, artifact, 0o600)
			os.Remove(filepath.Join(dir, ArtifactFile))
			os.Symlink(target, filepath.Join(dir, ArtifactFile))
			return dir
		}, trustFor(key), "not a regular file"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBundle(tc.prepare(t), validator, tc.trust)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected rejection containing %q, got %v", tc.want, err)
			}
		})
	}
}

// TestCommandSignatureCannotVerifyAsManifest checks domain separation: the
// same key signing the same canonical bytes under the command domain must not
// produce a valid manifest signature.
func TestCommandSignatureCannotVerifyAsManifest(t *testing.T) {
	key := testKey(0)
	dir := writeBundle(t, key, []byte("x"), nil)
	raw, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	message, _, _, err := signingInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	foreign := append([]byte("OpenUMP command v1\n"), message[len(manifestDomain):]...)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["signature"].(map[string]any)["value"] = encodeSignature(ed25519.Sign(key, foreign))
	encoded, _ := json.Marshal(m)
	if err := VerifyManifest(encoded, trustFor(key)); err == nil {
		t.Fatal("a command-domain signature must not verify as a manifest signature")
	}
}

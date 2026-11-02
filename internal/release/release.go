// Package release signs and verifies Hades release artifacts and generates
// third-party notices. Signatures are raw 64-byte Ed25519 over checksums.txt,
// with SPKI PEM keys, so a host can verify them with only OpenSSL 3:
//
//	openssl pkeyutl -verify -pubin -inkey release.pub -rawin -in checksums.txt -sigfile checksums.txt.sig
package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GenerateKey writes a new Ed25519 release key pair (PKCS#8 private, SPKI
// public) and refuses to overwrite an existing private key.
func GenerateKey(privatePath, publicPath string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(privatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return WritePublicKey(publicPath, pub)
}

// PublicKeyPEM encodes an SPKI PEM public key.
func PublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// WritePublicKey writes an SPKI PEM public key.
func WritePublicKey(path string, pub ed25519.PublicKey) error {
	raw, err := PublicKeyPEM(pub)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// LoadPrivateKey reads a PKCS#8 PEM Ed25519 private key from a private file.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: release signing key must not be group or world readable", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: not a PEM private key", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 key", path)
	}
	return priv, nil
}

// LoadPublicKey reads an SPKI PEM Ed25519 public key.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%s: not a PEM public key", path)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 key", path)
	}
	return pub, nil
}

// Checksums writes a sha256sum-compatible checksums.txt for the named files in
// dir, sorted by name, and returns its bytes.
func Checksums(dir string, names []string) ([]byte, error) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var b strings.Builder
	for _, name := range sorted {
		if strings.ContainsAny(name, "/\\\n ") {
			return nil, fmt.Errorf("artifact name %q must be a plain file name", name)
		}
		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s  %s\n", sum, name)
	}
	return []byte(b.String()), nil
}

// Sign returns the raw Ed25519 signature of message.
func Sign(key ed25519.PrivateKey, message []byte) []byte { return ed25519.Sign(key, message) }

// Verify checks a raw Ed25519 signature and then every checksum line against
// the files in dir. Nothing listed is trusted until the signature verifies.
func Verify(pub ed25519.PublicKey, dir string, checksums, signature []byte) error {
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(pub, checksums, signature) {
		return fmt.Errorf("release signature does not verify: checksums.txt is not from the release key")
	}
	lines := strings.Split(strings.TrimRight(string(checksums), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return fmt.Errorf("checksums.txt lists no artifacts")
	}
	for _, line := range lines {
		want, name, ok := strings.Cut(line, "  ")
		if !ok || len(want) != 64 || strings.ContainsAny(name, "/\\") {
			return fmt.Errorf("malformed checksum line %q", line)
		}
		got, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if got != want {
			return fmt.Errorf("%s: SHA-256 mismatch", name)
		}
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

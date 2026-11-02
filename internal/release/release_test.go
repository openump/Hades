package release

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/compliance"
)

func keys(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	priv, pub := filepath.Join(dir, "release.key"), filepath.Join(dir, "release.pub")
	if err := GenerateKey(priv, pub); err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func release(t *testing.T) (dir string, sums, sig []byte, pub ed25519.PublicKey) {
	t.Helper()
	privPath, pubPath := keys(t)
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "hades_1.0.0_linux_amd64.tar.gz"), []byte("archive"), 0o644)
	os.WriteFile(filepath.Join(dir, "hades_1.0.0_linux_amd64.deb"), []byte("package"), 0o644)
	sums, err := Checksums(dir, []string{"hades_1.0.0_linux_amd64.tar.gz", "hades_1.0.0_linux_amd64.deb"})
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := LoadPrivateKey(privPath)
	pub, _ = LoadPublicKey(pubPath)
	return dir, sums, Sign(priv, sums), pub
}

func TestSignedReleaseVerifiesAndTamperingIsRefused(t *testing.T) {
	dir, sums, sig, pub := release(t)
	if err := Verify(pub, dir, sums, sig); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(sums), strings.Split(string(sums), "\n")[0]) || !strings.Contains(string(sums), "  hades_1.0.0_linux_amd64.deb\n") {
		t.Fatal("checksums must be sha256sum format:", string(sums))
	}
	cases := map[string]func() ([]byte, []byte, ed25519.PublicKey){
		"tampered file": func() ([]byte, []byte, ed25519.PublicKey) {
			os.WriteFile(filepath.Join(dir, "hades_1.0.0_linux_amd64.deb"), []byte("evil"), 0o644)
			return sums, sig, pub
		},
		"tampered checksums": func() ([]byte, []byte, ed25519.PublicKey) {
			return bytes.Replace(sums, []byte(sums[:1]), []byte("0"), 1), sig, pub
		},
		"truncated signature": func() ([]byte, []byte, ed25519.PublicKey) { return sums, sig[:32], pub },
		"another key": func() ([]byte, []byte, ed25519.PublicKey) {
			other, _, _ := ed25519.GenerateKey(nil)
			return sums, sig, other
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d, s, g, p := release(t)
			dir = d
			sums, sig, pub = s, g, p
			s2, g2, p2 := mutate()
			if err := Verify(p2, d, s2, g2); err == nil {
				t.Fatal("must be refused")
			}
		})
	}
}

func TestPrivateKeyMustBePrivate(t *testing.T) {
	priv, _ := keys(t)
	os.Chmod(priv, 0o644)
	if _, err := LoadPrivateKey(priv); err == nil {
		t.Fatal("a world-readable release key must be refused")
	}
	if err := GenerateKey(priv, priv+".pub"); err == nil {
		t.Fatal("keygen must never overwrite an existing private key")
	}
}

// TestOpenSSLVerifiesGoSignatures proves the format install.sh relies on: a Go
// Ed25519 signature over checksums.txt verifies with plain OpenSSL 3.
func TestOpenSSLVerifiesGoSignatures(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir, sums, sig, _ := release(t)
	privPath, pubPath := keys(t)
	priv, _ := LoadPrivateKey(privPath)
	sig = Sign(priv, sums)
	os.WriteFile(filepath.Join(dir, "checksums.txt"), sums, 0o644)
	os.WriteFile(filepath.Join(dir, "checksums.txt.sig"), sig, 0o644)
	verify := func() error {
		return exec.Command("openssl", "pkeyutl", "-verify", "-pubin", "-inkey", pubPath, "-rawin",
			"-in", filepath.Join(dir, "checksums.txt"), "-sigfile", filepath.Join(dir, "checksums.txt.sig")).Run()
	}
	if err := verify(); err != nil {
		t.Skip("this OpenSSL cannot verify Ed25519 raw signatures:", err)
	}
	os.WriteFile(filepath.Join(dir, "checksums.txt"), append(sums, 'x'), 0o644)
	if verify() == nil {
		t.Fatal("openssl must reject a tampered checksums.txt")
	}
}

func TestTarballIsReproducible(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "hades"), []byte("binary"), 0o755)
	os.WriteFile(filepath.Join(src, "LICENSE"), []byte("license"), 0o600)
	os.Symlink("hades", filepath.Join(src, "hades-normalizer"))
	epoch := time.Unix(1792152000, 0)
	a, b := filepath.Join(t.TempDir(), "a.tar.gz"), filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Tarball(a, src, "hades_1.0.0_linux_amd64", epoch); err != nil {
		t.Fatal(err)
	}
	// Change on-disk times and permissions: the archive must not change.
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(src, "LICENSE"), later, later)
	os.Chmod(filepath.Join(src, "LICENSE"), 0o640)
	if err := Tarball(b, src, "hades_1.0.0_linux_amd64", epoch); err != nil {
		t.Fatal(err)
	}
	x, _ := os.ReadFile(a)
	y, _ := os.ReadFile(b)
	if !bytes.Equal(x, y) {
		t.Fatal("the same inputs must give byte-identical archives regardless of on-disk mtimes and modes")
	}
	out, err := exec.Command("tar", "-tvzf", a).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	if !strings.Contains(string(out), "hades-normalizer -> hades") {
		t.Fatal("symlinks must be kept:", string(out))
	}
}

func TestNoticesFailClosedWithoutLicenseText(t *testing.T) {
	bom := &compliance.BOM{Components: []compliance.Component{{Type: "library", Name: "example.com/unlicensed", Version: "v1.0.0"}}}
	goroot := t.TempDir()
	os.WriteFile(filepath.Join(goroot, "LICENSE"), []byte("Go BSD license text"), 0o644)
	if _, err := Notices(bom, t.TempDir(), goroot); err == nil || !strings.Contains(err.Error(), "example.com/unlicensed") {
		t.Fatal("a linked module without a license text must fail the notices:", err)
	}
	cache := t.TempDir()
	mod := filepath.Join(cache, "github.com/!acme/thing@v1.0.0")
	os.MkdirAll(mod, 0o755)
	os.WriteFile(filepath.Join(mod, "LICENSE-APACHE-2.0.txt"), []byte("Apache License text"), 0o644)
	bom = &compliance.BOM{Components: []compliance.Component{{Type: "library", Name: "github.com/Acme/thing", Version: "v1.0.0", Licenses: []compliance.License{{Expression: "Apache-2.0"}}}}}
	text, err := Notices(bom, cache, goroot)
	if err != nil || !strings.Contains(text, "Apache License text") || !strings.Contains(text, "License: Apache-2.0") || !strings.Contains(text, "Go BSD license text") {
		t.Fatalf("license text must be found via the escaped cache path and any LICENSE* file: %v\n%s", err, text)
	}
}

// TestEveryLinkedModuleOfHadesHasAReviewedLicense keeps the reviewed license
// map complete: a new linked dependency without an entry fails here.
func TestEveryLinkedModuleOfHadesHasAReviewedLicense(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "hades")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/hades").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	raw, _ := os.ReadFile(bin)
	bom, err := compliance.BinarySBOM(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range bom.Components {
		if c.Type == "library" && len(c.Licenses) == 0 {
			t.Errorf("%s %s is linked into hades but has no reviewed license in internal/compliance", c.Name, c.Version)
		}
	}
}

func TestSBOMCarriesTheReleaseVersion(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "hades")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "../../cmd/hades").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	raw, _ := os.ReadFile(bin)
	bom, err := compliance.BinarySBOM(raw, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if bom.Metadata.Component.Version != "1.2.3" {
		t.Fatal("the SBOM subject must carry the release version:", bom.Metadata.Component)
	}
	for _, c := range bom.Components {
		if c.Type == "application" && (c.Version != "1.2.3" || !strings.HasSuffix(c.PURL, "@1.2.3")) {
			t.Fatal("the application component must carry the release version, not (devel):", c)
		}
	}
	if len(bom.Dependencies) != 1 || !strings.HasSuffix(bom.Dependencies[0].Ref, "@1.2.3") {
		t.Fatal("dependencies must reference the versioned application:", bom.Dependencies)
	}
}

func TestTarballFailureLeavesNothingToSign(t *testing.T) {
	out := filepath.Join(t.TempDir(), "missing-dir", "x.tar.gz")
	if err := Tarball(out, t.TempDir(), "p", time.Unix(0, 0)); err == nil {
		t.Fatal("an unwritable target must fail")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a failed archive must not be left behind")
	}
}

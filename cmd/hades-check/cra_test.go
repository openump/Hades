package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openump/Hades/internal/site"
)

// release builds a real secure release tree with a site.Prepare manifest.
func release(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, path := range site.Required("secure", "examples/site/metadata.json") {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755)
		if path == "artifacts/hades-history-proxy" {
			if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, path), "../hades-history-proxy").CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, path), raw, 0o644)
	}
	pki := filepath.Join(t.TempDir(), "pki")
	if out, err := exec.Command("go", "run", "../hades-pki", "-out", pki).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	m, err := site.Prepare(dir, "secure-a", "hades-cra-cli", "secure", "examples/site/metadata.json", map[string]string{"HADES_SECURITY_DIR": pki}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "site.json"), raw, 0o644)
	return dir
}

func craReport(t *testing.T, root string, extra ...string) (string, string, error) {
	t.Helper()
	out := t.TempDir()
	args := append([]string{"run", ".", "-root", root, "-cra-report", "-site-config", "site.json", "-out-dir", out}, extra...)
	stdout, err := exec.Command("go", args...).CombinedOutput()
	return out, string(stdout), err
}

func TestCRAReportPassesForSecureRelease(t *testing.T) {
	root := release(t)
	out, stdout, err := craReport(t, root)
	if err != nil {
		t.Fatal(err, stdout)
	}
	for _, name := range []string{"sbom.cdx.json", "cra-attestation.json", "cra-compliance-report.md"} {
		if st, err := os.Stat(filepath.Join(out, name)); err != nil || st.Size() == 0 {
			t.Fatal("missing", name)
		}
	}
	var a struct {
		Passed bool `json:"passed"`
	}
	raw, _ := os.ReadFile(filepath.Join(out, "cra-attestation.json"))
	if json.Unmarshal(raw, &a); !a.Passed {
		t.Fatal("secure release must pass:", stdout)
	}
}

func TestCRAReportFailsWithNonZeroExit(t *testing.T) {
	t.Run("anonymous broker", func(t *testing.T) {
		root := release(t)
		cfg := filepath.Join(root, "config/broker/security/mosquitto.conf")
		raw, _ := os.ReadFile(cfg)
		os.WriteFile(cfg, []byte(strings.Replace(string(raw), "allow_anonymous false", "allow_anonymous true", 1)), 0o644)
		out, stdout, err := craReport(t, root)
		if err == nil {
			t.Fatal("anonymous broker must exit non-zero:", stdout)
		}
		report, _ := os.ReadFile(filepath.Join(out, "cra-compliance-report.md"))
		if !strings.Contains(string(report), "FAIL: allow_anonymous") {
			t.Fatal("the report must name the failed control:\n", string(report))
		}
	})
	t.Run("missing binary digest", func(t *testing.T) {
		root := release(t)
		_, stdout, err := craReport(t, root, "-binaries", "artifacts/hades-history-proxy,artifacts/hades-actuator")
		if err == nil || !strings.Contains(stdout, "FAIL") || !strings.Contains(stdout, "integrity-artifact-digests") {
			t.Fatal("a missing binary must exit non-zero:", stdout)
		}
	})
	t.Run("non-Go binary", func(t *testing.T) {
		root := release(t)
		os.WriteFile(filepath.Join(root, "artifacts/tool.sh"), []byte("#!/bin/sh\n"), 0o755)
		out, stdout, err := craReport(t, root, "-binaries", "artifacts/hades-history-proxy,artifacts/tool.sh")
		report, _ := os.ReadFile(filepath.Join(out, "cra-compliance-report.md"))
		if err == nil || !strings.Contains(string(report), "artifacts/tool.sh: no Go build information") {
			t.Fatal("an uninventoriable binary must fail with the reason recorded:", stdout)
		}
	})
	t.Run("missing arguments", func(t *testing.T) {
		if _, stdout, err := craReport(t, t.TempDir(), "-site-config", ""); err == nil {
			t.Fatal("missing -site-config must fail:", stdout)
		}
	})
}

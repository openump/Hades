package compliance

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/site"
)

// evalTime is fixed for one run so repeated evaluations are comparable; the
// hades-pki identities are issued at the real current time.
var evalTime = time.Now().UTC().Truncate(time.Second)

var proxyBinary, inspectorBinary string

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "cra-bin")
	proxyBinary, inspectorBinary = filepath.Join(dir, "hades-history-proxy"), filepath.Join(dir, "hades-inspector")
	for out, pkg := range map[string]string{proxyBinary: "../../cmd/hades-history-proxy", inspectorBinary: "../../cmd/hades-inspector"} {
		if raw, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			os.Stderr.Write(raw)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixtureSite builds a real secure release: the repository's reviewed files,
// real built binaries, hades-pki identities and a site.Prepare manifest.
func fixtureSite(t *testing.T) Site {
	t.Helper()
	dir := t.TempDir()
	put := func(dst string, raw []byte) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, dst)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, dst), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(src string) []byte {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, path := range site.Required("secure", "examples/site/metadata.json") {
		if path == "artifacts/hades-history-proxy" {
			put(path, read(proxyBinary))
			continue
		}
		put(path, read(filepath.Join("../..", path)))
	}
	put("artifacts/hades-inspector", read(inspectorBinary))
	pki := filepath.Join(t.TempDir(), "pki")
	if out, err := exec.Command("go", "run", "../../cmd/hades-pki", "-out", pki).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	m, err := site.Prepare(dir, "secure-a", "hades-cra-test", "secure", "examples/site/metadata.json", map[string]string{"HADES_SECURITY_DIR": pki}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	put("site.json", raw)
	s, err := FromManifest(dir, "site.json", []string{"artifacts/hades-history-proxy", "artifacts/hades-inspector"}, "", evalTime)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSBOMIsCycloneDXWithLinkedModulesAndLicenses(t *testing.T) {
	s := fixtureSite(t)
	_, bom := Evaluate(s)
	if bom.Format != "CycloneDX" || bom.SpecVersion != "1.5" {
		t.Fatal(bom.Format, bom.SpecVersion)
	}
	byName := map[string]Component{}
	for _, c := range bom.Components {
		byName[c.Name] = c
		if c.PURL == "" || c.BOMRef == "" || !strings.HasPrefix(c.PURL, "pkg:golang/") {
			t.Fatalf("component missing purl/bom-ref: %+v", c)
		}
	}
	paho, ok := byName["github.com/eclipse/paho.golang"]
	if !ok || paho.Version != "v0.23.0" || len(paho.Hashes) != 1 || len(paho.Hashes[0].Content) != 64 {
		t.Fatalf("linked module with its go.sum hash expected: %+v", paho)
	}
	if paho.Licenses[0].Expression != "EPL-2.0" {
		t.Fatal("license identifier expected:", paho.Licenses)
	}
	if _, linked := byName["github.com/apache/plc4x/plc4go"]; linked {
		t.Fatal("the SBOM must list only modules linked into the binary, not every go.mod entry")
	}
	for _, c := range bom.Components {
		if c.Type == "application" && (len(c.Licenses) == 0 || c.Licenses[0].Expression != "Apache-2.0") {
			t.Fatalf("Hades binaries carry the module license: %+v", c)
		}
	}
	// hades-history-proxy links only the standard library; the inspector links modules.
	var inspector *Dependency
	for i := range bom.Dependencies {
		if strings.Contains(bom.Dependencies[i].Ref, "/cmd/hades-inspector@") {
			inspector = &bom.Dependencies[i]
		}
	}
	if len(bom.Dependencies) != 2 || inspector == nil || len(inspector.DependsOn) == 0 {
		t.Fatal("each application's dependencies must be recorded", bom.Dependencies)
	}
	seen := map[string]bool{}
	for _, ref := range inspector.DependsOn {
		if seen[ref] {
			t.Fatal("binaries sharing modules must not duplicate dependsOn:", ref)
		}
		seen[ref] = true
	}
}

func TestCompliantSitePassesDeterministically(t *testing.T) {
	s := fixtureSite(t)
	a, bom := Evaluate(s)
	for _, c := range a.Controls {
		if c.Outcome == Fail || (c.Mandatory && c.Outcome != Pass) {
			t.Errorf("%s: %s %v", c.ID, c.Outcome, c.Evidence)
		}
	}
	if !a.Passed {
		t.Fatal("the secure reference site must pass")
	}
	out1, out2 := t.TempDir(), t.TempDir()
	if err := Write(out1, a, bom); err != nil {
		t.Fatal(err)
	}
	a2, bom2 := Evaluate(s)
	if err := Write(out2, a2, bom2); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sbom.cdx.json", "cra-attestation.json", "cra-compliance-report.md"} {
		x, _ := os.ReadFile(filepath.Join(out1, name))
		y, _ := os.ReadFile(filepath.Join(out2, name))
		if len(x) == 0 || !bytes.Equal(x, y) {
			t.Fatalf("%s is not deterministic", name)
		}
	}
	report, _ := os.ReadFile(filepath.Join(out1, "cra-compliance-report.md"))
	if !strings.Contains(string(report), "not a conformity assessment") {
		t.Fatal("every report must carry the non-certification disclaimer")
	}
}

func TestViolationsAreExplicitFailures(t *testing.T) {
	cases := map[string]struct {
		mutate  func(t *testing.T, s *Site)
		control string
		want    string
	}{
		"not yet valid": {func(t *testing.T, s *Site) { s.Now = evalTime.AddDate(0, 0, -2) }, "identity-certificate-validity", "not valid until"},
		"anonymous broker": {func(t *testing.T, s *Site) {
			edit(t, s, s.MosquittoCfg, "allow_anonymous false", "allow_anonymous true")
		}, "identity-no-anonymous", "allow_anonymous"},
		"tls 1.2 broker": {func(t *testing.T, s *Site) {
			edit(t, s, s.MosquittoCfg, "tls_version tlsv1.3", "tls_version tlsv1.2")
		}, "transport-tls13", "tlsv1.2"},
		"missing binary":       {func(t *testing.T, s *Site) { os.Remove(filepath.Join(s.Dir, "artifacts/hades-inspector")) }, "integrity-artifact-digests", "missing"},
		"no binaries supplied": {func(t *testing.T, s *Site) { s.Binaries = nil }, "integrity-artifact-digests", "no deployed binaries"},
		"telemetry role can publish commands": {func(t *testing.T, s *Site) {
			edit(t, s, s.RolesJSON, `"rolename": "normalizer",
      "acls": [`, `"rolename": "normalizer",
      "acls": [
        {"acltype": "publishClientSend", "topic": "openump/v1/acme/berlin/assembly/line-01/press-04/command/+", "allow": true},`)
		}, "authz-least-privilege", "non-control role normalizer"},
		"wildcard grant": {func(t *testing.T, s *Site) {
			edit(t, s, s.RolesJSON, `"rolename": "normalizer",
      "acls": [`, `"rolename": "normalizer",
      "acls": [
        {"acltype": "subscribeLiteral", "topic": "openump/v1/#", "allow": true},`)
		}, "authz-least-privilege", "wildcard"},
		"no manifest": {func(t *testing.T, s *Site) { s.Manifest = "" }, "integrity-site-configuration", "no reviewed site manifest"},
		"release drifted from manifest": {func(t *testing.T, s *Site) {
			edit(t, s, "config/normalization/temperature.blobl", "", "# drift\n")
		}, "integrity-site-configuration", "digest mismatch"},
		"expired CA and leaves": {func(t *testing.T, s *Site) { s.Now = evalTime.AddDate(30, 0, 0) }, "identity-certificate-validity", "expired"},
		"deeper single-level wildcard publishing commands": {func(t *testing.T, s *Site) {
			addACL(t, s, "normalizer", "publishClientSend", "openump/v1/acme/berlin/assembly/line-01/press-04/+/+")
		}, "authz-least-privilege", "wildcard grant"},
		"deeper single-level wildcard subscription": {func(t *testing.T, s *Site) {
			addACL(t, s, "consumer-secondary", "subscribePattern", "openump/v1/acme/+/assembly/line-01/press-04/telemetry")
		}, "authz-least-privilege", "wildcard grant"},
		"wildcard command publish without channel name": {func(t *testing.T, s *Site) {
			addACL(t, s, "hades-infer", "publishClientSend", "openump/v1/acme/berlin/assembly/line-01/+/command/x")
		}, "authz-least-privilege", "non-control role hades-infer"},
		"non-admin role can rewrite ACLs": {func(t *testing.T, s *Site) {
			addACL(t, s, "normalizer", "publishClientSend", "$CONTROL/dynamic-security/v1")
		}, "authz-least-privilege", "non-admin role normalizer"},
		"extra plaintext listener": {func(t *testing.T, s *Site) {
			edit(t, s, s.MosquittoCfg, "enable_control_api false", "enable_control_api false\nlistener 1883 0.0.0.0")
		}, "transport-tls13", "listener 1883 has no certfile"},
		"per-listener settings": {func(t *testing.T, s *Site) {
			edit(t, s, s.MosquittoCfg, "", "per_listener_settings true\n")
		}, "identity-no-anonymous", "per_listener_settings"},
		"leaf chained to a rogue CA in the credential directory": {func(t *testing.T, s *Site) {
			rogue := filepath.Join(t.TempDir(), "rogue")
			if out, err := exec.Command("go", "run", "../../cmd/hades-pki", "-out", rogue).CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			dir := filepath.Dir(s.CA)
			for _, f := range []string{"ca.crt", "normalizer.crt"} {
				raw, _ := os.ReadFile(filepath.Join(rogue, f))
				os.WriteFile(filepath.Join(dir, "zz-rogue-"+f), raw, 0o644)
			}
			s.Certificates = append(s.Certificates, filepath.Join(dir, "zz-rogue-ca.crt"), filepath.Join(dir, "zz-rogue-normalizer.crt"))
		}, "identity-certificate-validity", "zz-rogue-normalizer.crt does not chain to the site CA"},
		"non-Go binary": {func(t *testing.T, s *Site) {
			os.WriteFile(filepath.Join(s.Dir, "artifacts/tool.sh"), []byte("#!/bin/sh\n"), 0o755)
			s.Binaries = append(s.Binaries, "artifacts/tool.sh")
		}, "integrity-artifact-digests", "no Go build information"},
		"unbounded queue": {func(t *testing.T, s *Site) {
			edit(t, s, s.MosquittoCfg, "max_queued_messages 4096", "max_queued_messages 0")
		}, "resilience-resource-limits", "max_queued_messages"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := fixtureSite(t)
			tc.mutate(t, &s)
			a, _ := Evaluate(s)
			if a.Passed {
				t.Fatal("a violating site must not pass")
			}
			var found *Control
			for _, c := range a.Controls {
				if c.ID == tc.control {
					found = c
				}
			}
			if found == nil || found.Outcome != Fail || !strings.Contains(strings.Join(found.Evidence, " "), tc.want) {
				t.Fatalf("expected %s to FAIL mentioning %q, got %+v", tc.control, tc.want, found)
			}
		})
	}
}

func TestCommentedDirectiveDoesNotFail(t *testing.T) {
	s := fixtureSite(t)
	edit(t, &s, s.MosquittoCfg, "", "# no acl_file: dynamic security only\n")
	reprepare(t, &s)
	if a, _ := Evaluate(s); !a.Passed {
		for _, c := range a.Controls {
			t.Log(c.ID, c.Outcome, c.Evidence)
		}
		t.Fatal("a comment must not count as a directive")
	}
}

// reprepare records a reviewed change in a new manifest revision.
func reprepare(t *testing.T, s *Site) {
	t.Helper()
	raw, _ := os.ReadFile(s.path(s.Manifest))
	old, _ := site.Decode(raw)
	m, err := site.Prepare(s.Dir, "secure-b", old.Project, "secure", old.Metadata, old.Secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(m)
	os.WriteFile(s.path(s.Manifest), raw, 0o644)
}

func addACL(t *testing.T, s *Site, role, acltype, topic string) {
	t.Helper()
	p := s.path(s.RolesJSON)
	raw, _ := os.ReadFile(p)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, r := range doc["roles"].([]any) {
		r := r.(map[string]any)
		if r["rolename"] == role {
			r["acls"] = append(r["acls"].([]any), map[string]any{"acltype": acltype, "topic": topic, "allow": true})
			out, _ := json.Marshal(doc)
			os.WriteFile(p, out, 0o644)
			return
		}
	}
	t.Fatal("no role", role)
}

func edit(t *testing.T, s *Site, rel, old, new string) {
	t.Helper()
	p := s.path(rel)
	raw, _ := os.ReadFile(p)
	if old != "" && !strings.Contains(string(raw), old) {
		t.Fatalf("%s does not contain %q", rel, old)
	}
	if old == "" {
		os.WriteFile(p, append([]byte(new), raw...), 0o644)
		return
	}
	os.WriteFile(p, []byte(strings.Replace(string(raw), old, new, 1)), 0o644)
}

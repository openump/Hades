package site

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, Manifest) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Prepare(root, "lab-a", "hades-test-site", "dev", "examples/site/metadata.json", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return root, m
}
func TestConfigurationValidationAndImmutableStage(t *testing.T) {
	root, m := fixture(t)
	if err := Validate(root, m); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir() + "/release"
	if err := Stage(root, dir, m); err != nil {
		t.Fatal(err)
	}
	m.Files["config/broker/mosquitto.conf"] = strings.Repeat("0", 64)
	if err := Validate(root, m); err == nil {
		t.Fatal("changed artifact accepted")
	}
	if err := Stage(root, dir, m); err == nil {
		t.Fatal("existing release overwritten")
	}
}
func TestRejectUnsafeConfigurationAndMissingSecrets(t *testing.T) {
	root, m := fixture(t)
	for _, mutate := range []func(*Manifest){func(m *Manifest) { m.Revision = "../escape" }, func(m *Manifest) { m.Profile = "unknown" }, func(m *Manifest) { m.Files["../secret"] = strings.Repeat("0", 64) }, func(m *Manifest) { m.Metadata = "missing.json" }, func(m *Manifest) { m.Ports = map[string]string{"HADES_MQTT_PORT": "not-a-port"} }, func(m *Manifest) { m.Secrets = map[string]string{"HADES_SECURITY_DIR": "relative"} }} {
		raw, _ := json.Marshal(m)
		var candidate Manifest
		json.Unmarshal(raw, &candidate)
		mutate(&candidate)
		if err := Validate(root, candidate); err == nil {
			t.Fatal("invalid manifest accepted", candidate)
		}
	}
}
func TestApplyDoesNotCommitOnFailureAndReportsLiveRevision(t *testing.T) {
	root, m := fixture(t)
	dir := t.TempDir() + "/site"
	fail := false
	live := map[string]string{}
	runner := func(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "config --quiet") {
			return nil, nil
		}
		if strings.Contains(strings.Join(args, " "), "up --build") {
			if fail {
				return nil, os.ErrPermission
			}
			live = map[string]string{"revision": m.Revision, "digest": Digest(m)}
			return nil, nil
		}
		if len(args) > 0 && args[0] == "inspect" {
			return json.Marshal([]any{map[string]any{"State": map[string]any{"Running": true}, "Config": map[string]any{"Labels": map[string]string{"org.openump.config-revision": live["revision"], "org.openump.config-digest": live["digest"]}}}})
		}
		return []byte("container-id\n"), nil
	}
	manager, err := Open(dir, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err = manager.Apply(context.Background(), root, m); err != nil {
		t.Fatal(err)
	}
	active, err := manager.Active()
	if err != nil || active.Revision != "lab-a" {
		t.Fatal(active, err)
	}
	m.Revision = "lab-b"
	fail = true
	if err = manager.Apply(context.Background(), root, m); err == nil {
		t.Fatal("failed deployment committed")
	}
	active, err = manager.Active()
	if err != nil || active.Revision != "lab-a" {
		t.Fatal(active, err)
	}
}

func TestSecretReferencesMustMatchTrustedServerCertificates(t *testing.T) {
	root, _ := fixture(t)
	source := t.TempDir()
	for _, path := range Required("secure", "examples/site/metadata.json") {
		if path == "artifacts/hades-history-proxy" {
			if err := write(filepath.Join(source, path), []byte("fixture binary")); err != nil {
				t.Fatal(err)
			}
			continue
		}
		raw, err := Read(root, path, MaxFile)
		if err != nil {
			t.Fatal(err)
		}
		if err = write(filepath.Join(source, path), raw); err != nil {
			t.Fatal(err)
		}
	}
	certdir := t.TempDir() + "/pki"
	command := exec.Command("go", "run", "../../cmd/hades-pki", "-out", certdir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	m, err := Prepare(source, "secure-a", "hades-secure-test", "secure", "examples/site/metadata.json", map[string]string{"HADES_SECURITY_DIR": certdir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(certdir+"/broker.key", 0644); err != nil {
		t.Fatal(err)
	}
	if err = Validate(source, m); err == nil {
		t.Fatal("public private-key permissions accepted")
	}
	os.Chmod(certdir+"/broker.key", 0600)
	// A valid certificate pair must also chain to the referenced CA.
	another := t.TempDir() + "/pki"
	command = exec.Command("go", "run", "../../cmd/hades-pki", "-out", another)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	raw, _ := os.ReadFile(another + "/ca.crt")
	os.WriteFile(certdir+"/ca.crt", raw, 0644)
	if err = Validate(source, m); err == nil {
		t.Fatal("untrusted certificate chain accepted")
	}
}

package actuator

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/openump/Hades/internal/supervisor"
)

func TestSupervisedActuatorLiveStatusAndKill(t *testing.T) {
	broker, pki := os.Getenv("HADES_ACTUATOR_TEST_BROKER"), os.Getenv("HADES_ACTUATOR_TEST_PKI")
	if broker == "" || pki == "" {
		t.Skip("explicit disposable mTLS broker required")
	}
	f := setup(t, "L0")
	dir, err := os.MkdirTemp("/tmp", "h33-supervised-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "hades-actuator")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hades-actuator")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	write := func(name string, raw []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	policy := write("policy.json", f.o.Policy)
	public, _ := json.Marshal(map[string]string{"operator-key": base64.StdEncoding.EncodeToString(f.o.Keys["operator-key"])})
	keys := write("keys.json", public)
	private := write("signing-key", []byte(base64.StdEncoding.EncodeToString(f.o.SigningKey)))
	state := filepath.Join(dir, "actuator")
	args := []string{"-state", state, "-root", root, "-config", filepath.Join(root, "examples/site/metadata.json"), "-policy", policy, "-keys", keys, "-receipt-key", private, "-receipt-key-id", "supervised-actuator-key", "-broker", broker, "-tls-ca", filepath.Join(pki, "ca.crt"), "-tls-cert", filepath.Join(pki, "hades-actuator.crt"), "-tls-key", filepath.Join(pki, "hades-actuator.key")}
	raw, _ := os.ReadFile(binary)
	cfg := supervisor.Config{Revision: "actuator-pilot", Binary: binary, BinarySHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Args: args, Directory: root, State: filepath.Join(dir, "service"), Ready: "actuator_ready", Files: map[string]string{}, WritablePaths: []string{state}, LogBytes: 65536}
	files := []string{policy, keys, private, filepath.Join(root, "examples/site/metadata.json"), filepath.Join(pki, "ca.crt"), filepath.Join(pki, "hades-actuator.crt"), filepath.Join(pki, "hades-actuator.key")}
	schemas, _ := filepath.Glob(filepath.Join(root, "schemas/v1/*.json"))
	files = append(files, schemas...)
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Files[p] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	definition, _ := json.Marshal(cfg)
	path := write("service.json", definition)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, path, supervisor.Digest(cfg)) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("supervised actuator failed to stop")
		}
	}()
	ready := false
	for i := 0; i < 200; i++ {
		status, err := supervisor.Probe(context.Background(), cfg)
		if err == nil && status.Ready {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		raw, _ := os.ReadFile(filepath.Join(cfg.State, "events.log"))
		t.Fatalf("no digest-bound live readiness: %s", raw)
	}
	if _, err = Control(context.Background(), filepath.Join(state, "control.sock"), "kill", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(state, "shadow.lock")); err != nil {
		t.Fatal(err)
	}
}

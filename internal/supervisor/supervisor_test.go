package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChild(t *testing.T) {
	if os.Args[len(os.Args)-1] != "hades-child" {
		return
	}
	fmt.Println("child-ready")
	for {
		fmt.Println(strings.Repeat("health", 20))
		time.Sleep(10 * time.Millisecond)
	}
}
func TestLiveIdentityLogsAndShutdown(t *testing.T) {
	binary, _ := os.Executable()
	raw, _ := os.ReadFile(binary)
	dir, err := os.MkdirTemp("/tmp", "hsvc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := Config{Revision: "test-a", Binary: binary, BinarySHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Args: []string{"-test.run=^TestChild$", "hades-child"}, Directory: dir, State: dir + "/state", Ready: "child-ready", Files: map[string]string{}, LogBytes: 512}
	artifact := filepath.Join(dir, "approved.conf")
	if err = os.WriteFile(artifact, []byte("reviewed"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Files[artifact] = digest
	path := dir + "/service.json"
	raw, _ = json.Marshal(cfg)
	os.WriteFile(path, raw, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, path, Digest(cfg)) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := Probe(context.Background(), cfg)
		if err == nil && state.Ready {
			if state.ChildPID == 0 || state.Revision != "test-a" {
				t.Fatal(state)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("not ready", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	wrong := cfg
	wrong.Revision = "wrong"
	if _, err := Probe(context.Background(), wrong); err == nil {
		t.Fatal("wrong revision accepted")
	}
	changed := cfg
	changed.Revision = "unapplied"
	changedRaw, _ := json.Marshal(changed)
	if err := os.WriteFile(path, changedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), cfg); err == nil {
		t.Fatal("partially applied definition accepted")
	}
	original, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if _, err = Probe(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(artifact, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Probe(context.Background(), cfg); err == nil {
		t.Fatal("live status accepted artifact tampering")
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), cfg); err == nil {
		t.Fatal("dead process reported live")
	}
	for _, name := range []string{"events.log", "events.log.1"} {
		info, err := os.Stat(filepath.Join(cfg.State, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 512 || info.Mode().Perm() != 0600 {
			t.Fatal(info)
		}
	}
}
func TestChangedArtifactAndUnsafeUnitRejected(t *testing.T) {
	cfg := Config{Revision: "a", Binary: "/bin/echo", BinarySHA256: strings.Repeat("0", 64), Directory: t.TempDir(), State: t.TempDir() + "/state", Ready: "ready", LogBytes: 512}
	if err := Validate(cfg); err == nil {
		t.Fatal("changed binary accepted")
	}
	if _, err := Unit("bad\nname", "alice", "/bin/tool", "/tmp/config", strings.Repeat("a", 64)); err == nil {
		t.Fatal("unit injection accepted")
	}
}

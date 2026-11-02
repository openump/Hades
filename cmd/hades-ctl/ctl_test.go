package main

import (
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/inspect"
)

func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hades-ctl")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	return bin
}

func TestTopOnceAgainstInspector(t *testing.T) {
	m := inspect.NewModel(func() time.Time { return time.Now().UTC() })
	m.Declare("urn:openump:acme:berlin:press-04", "openump/v1/acme/berlin/assembly/line-01/press-04", map[string]time.Duration{"motor_temperature": time.Minute})
	m.Telemetry(map[string]any{"asset_id": "urn:openump:acme:berlin:press-04", "observed_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"measurements": map[string]any{"motor_temperature": map[string]any{"value": 64.5, "unit": "Cel", "quality": "good"}}})
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: inspect.NewHandler(m, inspect.NewBroadcaster(), time.Second, inspect.LoopbackHosts(l.Addr().String())...)}
	go srv.Serve(l)
	defer srv.Close()
	bin := build(t)

	out, err := exec.Command(bin, "top", "-once", "-inspector", l.Addr().String()).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "motor_temperature") || !strings.Contains(string(out), "GOOD") {
		t.Fatalf("-once: %v\n%s", err, out)
	}
	// Without a terminal (a pipe), top falls back to one frame instead of hanging.
	cmd := exec.Command(bin, "top", "-inspector", l.Addr().String())
	cmd.Stdin = strings.NewReader("")
	done := make(chan []byte, 1)
	go func() { o, _ := cmd.CombinedOutput(); done <- o }()
	select {
	case o := <-done:
		if !strings.Contains(string(o), "motor_temperature") || !strings.Contains(string(o), "ssh -t") {
			t.Fatalf("non-TTY output: %s", o)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("hades-ctl top hung without a terminal")
	}
	srv.Close()
	out, err = exec.Command(bin, "top", "-once", "-inspector", l.Addr().String()).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "DAEMON UNREACHABLE") {
		t.Fatalf("an unreachable inspector must show the banner and exit non-zero: %v\n%s", err, out)
	}
}

func TestTopRefusesRemoteAndBadFlags(t *testing.T) {
	bin := build(t)
	for _, args := range [][]string{
		{"top", "-once", "-inspector", "10.0.0.5:18091"},
		{"top", "-once", "-inspector", "https://127.0.0.1:18091"},
		{"top", "-interval", "1ms"},
		{"status"},
		{},
	} {
		if out, err := exec.Command(bin, args...).CombinedOutput(); err == nil {
			t.Fatalf("%v must fail: %s", args, out)
		}
	}
}

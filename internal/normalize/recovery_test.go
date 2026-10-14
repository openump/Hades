package normalize

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRecoveryNormalizerChild(t *testing.T) {
	dir := os.Getenv("HADES_NORMALIZER_CHILD")
	if dir == "" {
		return
	}
	e, _ := fixture(t)
	s, err := NewServer(e, dir, 1, 100000)
	if err != nil {
		panic(err)
	}
	raw, _ := os.ReadFile("../../examples/raw-observations/valid.json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/observations", bytes.NewReader(raw)))
	if w.Code != 202 {
		panic(w.Code)
	}
	var p map[string]any
	json.Unmarshal(raw, &p)
	fmt.Println(p["message_id"])
	time.Sleep(time.Minute)
}
func TestRecoveryVolatileAdmissionSIGKILL(t *testing.T) {
	dir := t.TempDir() + "/q"
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryNormalizerChild$")
	cmd.Env = append(os.Environ(), "HADES_NORMALIZER_CHILD="+dir)
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(out)
		if scan.Scan() {
			ready <- scan.Text()
		}
	}()
	var id string
	select {
	case id = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("admission timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	e, _ := fixture(t)
	s, err := NewServer(e, dir, 1, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.pending) != 0 {
		t.Fatal("invented recovery of volatile admission")
	}
	report, _ := json.Marshal(map[string]any{"scenario": "normalizer-volatile-sigkill", "produced": []string{id}, "recovered": []string{}, "lost": []string{id}, "boundary": "accepted_volatile"})
	t.Log("RECOVERY " + string(report))
}
func TestRecoveryBlockedPublisherKeepsOwnedRecord(t *testing.T) {
	e, _ := fixture(t)
	s, err := NewServer(e, t.TempDir()+"/q", 1, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, _ := os.ReadFile("../../examples/raw-observations/valid.json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/observations", bytes.NewReader(raw)))
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, func(c context.Context, p []byte) error { close(entered); <-c.Done(); return c.Err() })
	}()
	<-entered
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/observations", bytes.NewReader(raw)))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	cancel()
	<-done
	if len(s.pending) != 1 || s.capacityRefusals != 1 {
		t.Fatal("blocked publisher retired owned data")
	}
	t.Log(`RECOVERY {"scenario":"blocked-publication","owned":1,"retired":0,"refused":1}`)
}

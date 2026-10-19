package normalize

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
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
	go s.Run(context.Background(), func(ctx context.Context, p []byte) error { fmt.Println("pending"); <-ctx.Done(); return ctx.Err() })
	server := httptest.NewServer(s)
	fmt.Println(server.URL)
	time.Sleep(time.Minute)
}
func TestRecoveryUnacknowledgedAdmissionSIGKILL(t *testing.T) {
	dir := t.TempDir() + "/q"
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryNormalizerChild$")
	cmd.Env = append(os.Environ(), "HADES_NORMALIZER_CHILD="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	lines := make(chan string, 4)
	go func() {
		scan := bufio.NewScanner(out)
		for scan.Scan() {
			lines <- scan.Text()
		}
	}()
	var url string
	select {
	case url = <-lines:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	result := make(chan int, 1)
	go func() {
		r, e := http.Post(url+"/observations", "application/json", strings.NewReader(string(rawFixture(t))))
		if e != nil {
			result <- 0
			return
		}
		defer r.Body.Close()
		result <- r.StatusCode
	}()
	select {
	case <-lines:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not start")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if code := <-result; code >= 200 && code < 300 {
		t.Fatal("lost volatile reading had been acknowledged", code)
	}
	e, _ := fixture(t)
	s, err := NewServer(e, dir, 1, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.pending) != 0 {
		t.Fatal("invented spool recovery")
	}
	report, _ := json.Marshal(map[string]any{"scenario": "normalizer-pre-puback-sigkill", "http_2xx": 0, "gateway_retains": true, "boundary": "accepted_broker_ack"})
	t.Log("RECOVERY " + string(report))
}

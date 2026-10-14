package normalize

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmissionQuarantineAndRetry(t *testing.T) {
	e, _ := fixture(t)
	server, err := NewServer(e, t.TempDir()+"/quarantine", 1, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, _ := os.ReadFile("../../examples/raw-observations/valid.json")
	request := func(body []byte) int {
		r := httptest.NewRequest(http.MethodPost, "/observations", bytes.NewReader(body))
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code
	}
	if request(raw) != 202 || request(raw) != 503 {
		t.Fatal("incorrect admission boundary")
	}
	if request([]byte(`{`)) != 400 || request([]byte(strings.Repeat("x", 65537))) != 413 {
		t.Fatal("incorrect wire rejection")
	}
	invalid := bytes.Replace(raw, []byte("deciCel"), []byte("unknown"), 1)
	if request(invalid) != 422 {
		t.Fatal("accepted wrong unit")
	}
	if len(server.quarantine.Records) != 3 {
		t.Fatal("diagnostics missing")
	}
	var attempts atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, func(_ context.Context, payload []byte) error {
			attempts.Add(1)
			if attempts.Load() == 1 {
				return context.DeadlineExceeded
			}
			cancel()
			return nil
		})
	}()
	<-done
	if attempts.Load() != 2 {
		t.Fatal(attempts.Load())
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.pending) != 0 {
		t.Fatal("PUBACK did not retire")
	}
}

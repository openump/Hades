package normalize

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func rawFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../examples/raw-observations/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func post(s *Server, raw []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/observations", bytes.NewReader(raw)))
	return w
}
func worker(t *testing.T, s *Server, publish func(context.Context, []byte) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, publish) }()
	t.Cleanup(func() { cancel(); <-done })
}
func TestSuccessWaitsForBrokerAcknowledgement(t *testing.T) {
	e, _ := fixture(t)
	s, err := NewServer(e, t.TempDir()+"/q", 1, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	worker(t, s, func(ctx context.Context, p []byte) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- post(s, rawFixture(t)) }()
	<-entered
	select {
	case <-done:
		t.Fatal("HTTP completed before PUBACK")
	case <-time.After(20 * time.Millisecond):
	}
	health := httptest.NewRecorder()
	s.ServeHTTP(health, httptest.NewRequest("GET", "/health", nil))
	if health.Code != 200 {
		t.Fatal(health.Code)
	}
	if w := post(s, rawFixture(t)); w.Code != 503 {
		t.Fatal("full queue did not backpressure", w.Code)
	}
	close(release)
	w := <-done
	if w.Code != 202 || !strings.Contains(w.Body.String(), "accepted_broker_ack") {
		t.Fatal(w.Code, w.Body.String())
	}
	stats := s.Stats()
	if stats["pending_records"] != 0 || stats["broker_accepted"] != uint64(1) || stats["capacity_refusals"] != uint64(1) {
		t.Fatal(stats)
	}
}
func TestFailedOrTimedOutPublishCannotAcknowledgeGateway(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "timeout"}[timeout], func(t *testing.T) {
			e, _ := fixture(t)
			s, err := NewServer(e, t.TempDir()+"/q", 10, 100000)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.publishTimeout = 50 * time.Millisecond
			worker(t, s, func(ctx context.Context, p []byte) error {
				if timeout {
					<-ctx.Done()
					return ctx.Err()
				}
				return errors.New("broker rejected")
			})
			w := post(s, rawFixture(t))
			if w.Code != 503 || w.Header().Get("Retry-After") == "" {
				t.Fatal(w.Code, w.Body.String())
			}
			if s.Stats()["broker_accepted"] != uint64(0) {
				t.Fatal(s.Stats())
			}
		})
	}
}
func TestInvalidInputStillQuarantinesWithoutBroker(t *testing.T) {
	e, _ := fixture(t)
	s, err := NewServer(e, t.TempDir()+"/q", 10, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range []struct {
		raw  []byte
		code int
	}{{[]byte("{"), 400}, {[]byte(strings.Repeat("x", 65537)), 413}, {bytes.Replace(rawFixture(t), []byte("deciCel"), []byte("unknown"), 1), 422}} {
		if w := post(s, c.raw); w.Code != c.code {
			t.Fatal(w.Code, c.code)
		}
	}
	if s.Stats()["quarantine_records"] != 3 || s.Stats()["pending_records"] != 0 {
		t.Fatal(s.Stats())
	}
}

func TestExpiredQueuedRequestIsNotPublished(t *testing.T) {
	e, _ := fixture(t)
	s, err := NewServer(e, t.TempDir()+"/q", 1, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.publishTimeout = 20 * time.Millisecond
	if w := post(s, rawFixture(t)); w.Code != 503 {
		t.Fatal(w.Code)
	}
	worker(t, s, func(context.Context, []byte) error { t.Error("expired request published"); return nil })
	deadline := time.Now().Add(time.Second)
	for s.Stats()["pending_records"] != 0 {
		if time.Now().After(deadline) {
			t.Fatal("expired request retained forever")
		}
		time.Sleep(time.Millisecond)
	}
	if s.Stats()["broker_accepted"] != uint64(0) {
		t.Fatal(s.Stats())
	}
}

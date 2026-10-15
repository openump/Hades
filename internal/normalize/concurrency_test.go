package normalize

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestQuarantineIOCannotBlockHealthOrPublish(t *testing.T) {
	e, _ := fixture(t)
	s, err := NewServer(e, t.TempDir()+"/q", 10, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.qmu.Lock()
	var flood sync.WaitGroup
	for range 8 {
		flood.Add(1)
		go func() {
			defer flood.Done()
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/observations", bytes.NewBufferString("{")))
		}()
	}
	time.Sleep(20 * time.Millisecond)
	health := make(chan struct{})
	go func() {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		close(health)
	}()
	select {
	case <-health:
	case <-time.After(300 * time.Millisecond):
		s.qmu.Unlock()
		t.Fatal("quarantine blocked health")
	}
	raw, _ := os.ReadFile("../../examples/raw-observations/valid.json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/observations", bytes.NewReader(raw)))
	if w.Code != 202 {
		s.qmu.Unlock()
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published := make(chan struct{}, 1)
	worker := make(chan error, 1)
	go func() {
		worker <- s.Run(ctx, func(context.Context, []byte) error { published <- struct{}{}; return nil })
	}()
	select {
	case <-published:
	case <-time.After(300 * time.Millisecond):
		s.qmu.Unlock()
		t.Fatal("quarantine blocked publication")
	}
	s.qmu.Unlock()
	flood.Wait()
	cancel()
	<-worker
}

func TestConcurrentMapping(t *testing.T) {
	e, _ := fixture(t)
	s, err := NewServer(e, t.TempDir()+"/q", 64, 1048576)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := os.ReadFile("../../examples/raw-observations/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var requests sync.WaitGroup
	for range 16 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/observations", bytes.NewReader(raw)))
			if w.Code != 202 {
				t.Errorf("concurrent mapping: %d", w.Code)
			}
		}()
	}
	requests.Wait()
	if s.Stats()["admitted"] != uint64(16) {
		t.Fatal(s.Stats())
	}
}

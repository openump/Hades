package normalize

import (
	"context"
	"net/http/httptest"
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
	worker(t, s, func(context.Context, []byte) error { return nil })
	s.qmu.Lock()
	var flood sync.WaitGroup
	for range 8 {
		flood.Add(1)
		go func() { defer flood.Done(); post(s, []byte("{")) }()
	}
	time.Sleep(20 * time.Millisecond)
	done := make(chan bool, 1)
	raw := rawFixture(t)
	go func() {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		done <- w.Code == 200 && post(s, raw).Code == 202
	}()
	select {
	case ok := <-done:
		if !ok {
			s.qmu.Unlock()
			t.Fatal("health/publication failed")
		}
	case <-time.After(500 * time.Millisecond):
		s.qmu.Unlock()
		t.Fatal("quarantine blocked publication")
	}
	s.qmu.Unlock()
	flood.Wait()
}
func TestConcurrentMapping(t *testing.T) {
	e, _ := fixture(t)
	s, err := NewServer(e, t.TempDir()+"/q", 64, 1048576)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	worker(t, s, func(context.Context, []byte) error { return nil })
	raw := rawFixture(t)
	var requests sync.WaitGroup
	for range 16 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			if w := post(s, raw); w.Code != 202 {
				t.Errorf("HTTP %d", w.Code)
			}
		}()
	}
	requests.Wait()
	if s.Stats()["broker_accepted"] != uint64(16) {
		t.Fatal(s.Stats())
	}
}

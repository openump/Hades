package history

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSinkStatusAndSuspendedOwnership(t *testing.T) {
	status := 400
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/import/prometheus" {
			t.Error(r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	sink, err := NewSink(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	cfg, raw := fixture(t)
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.Intake(cfg.Prefix+"telemetry", raw); err != nil {
		t.Fatal(err)
	}
	if err = a.Drain(context.Background(), sink.Write); err != nil {
		t.Fatal(err)
	}
	if a.Stats()["suspended"].(int) != 1 {
		t.Fatal(a.Stats())
	}
	var p map[string]any
	json.Unmarshal(raw, &p)
	if err = a.Resume(p["message_id"].(string)); err != nil {
		t.Fatal(err)
	}
	status = 204
	if err = a.Drain(context.Background(), sink.Write); err != nil {
		t.Fatal(err)
	}
	status = 429
	err = sink.Write(context.Background(), []byte("test"))
	var permanent *PermanentError
	if err == nil || errors.As(err, &permanent) {
		t.Fatal("429 must be temporary", err)
	}
	status = 403
	err = sink.Write(context.Background(), []byte("test"))
	if !errors.As(err, &permanent) {
		t.Fatal(err)
	}
	if _, err = NewSink("http://example.com"); err == nil {
		t.Fatal("remote address accepted")
	}
}

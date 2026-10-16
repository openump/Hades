package bridge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transfer"
	"github.com/openump/Hades/internal/transport"
	receiver "github.com/openump/Hades/tests/fixtures/cloud-receiver"
)

var certificates string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hades-bridge-pki-")
	if err != nil {
		panic(err)
	}
	certificates = filepath.Join(dir, "pki")
	command := exec.Command("go", "run", "../../../cmd/hades-pki", "-out", certificates, "-test-identities")
	if output, err := command.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("%s: %v", output, err))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
func credentials(t *testing.T, role string, server bool) *tls.Config {
	t.Helper()
	paths := transport.Paths{CA: certificates + "/ca.crt", Cert: certificates + "/" + role + ".crt", Key: certificates + "/" + role + ".key"}
	var cfg *tls.Config
	var err error
	if server {
		cfg, err = transport.Server(paths)
	} else {
		cfg, err = transport.Client(paths)
	}
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func fixture(t *testing.T) (*sample.Config, transfer.Record) {
	t.Helper()
	cfg, err := sample.Load("../../..", "../../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cfg.Observation("normal", 1, sample.UUID(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	json.Unmarshal(raw, &p)
	topic := cfg.Prefix + "telemetry"
	return cfg, transfer.Record{ID: p["message_id"].(string), Topic: topic, Payload: raw, Digest: transfer.Digest(topic, raw)}
}
func server(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	s.TLS = credentials(t, "cloud-receiver", true)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}
func sink(t *testing.T, s *httptest.Server, role string) *transfer.Sink {
	t.Helper()
	client, err := transfer.NewSink(s.URL+"/v1/ingest/observations", "acme", credentials(t, role, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}
func TestDurableReceiverReceiptLossRestartAndConflict(t *testing.T) {
	cfg, r := fixture(t)
	dir := t.TempDir() + "/receiver"
	recv, err := receiver.Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Persist and discard the first receipt, as if the connection disappeared
	// after commit. Close/reopen verifies the stable receipt lives on disk.
	var lost []byte
	s := server(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		recorder := httptest.NewRecorder()
		recv.ServeHTTP(recorder, req)
		lost = append([]byte(nil), recorder.Body.Bytes()...)
		http.Error(w, "lost", 503)
	}))
	client := sink(t, s, "transfer-adapter")
	if err = client.Send(context.Background(), r); err == nil {
		t.Fatal("lost receipt retired ownership")
	}
	s.Close()
	recv.Close()
	recv, err = receiver.Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	var replay []byte
	s = server(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		recorder := httptest.NewRecorder()
		recv.ServeHTTP(recorder, req)
		replay = append([]byte(nil), recorder.Body.Bytes()...)
		for k, v := range recorder.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorder.Code)
		w.Write(replay)
	}))
	client = sink(t, s, "transfer-adapter")
	if err = client.Send(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if string(replay) != string(lost) {
		t.Fatal("duplicate changed original receipt")
	}
	var entry map[string]any
	json.Unmarshal(r.Payload, &entry)
	entry["ingested_at"] = time.Now().UTC().Format(sample.Timestamp)
	r.Payload, _ = json.Marshal(entry)
	r.Digest = transfer.Digest(r.Topic, r.Payload)
	var permanent *transfer.PermanentError
	if err = client.Send(context.Background(), r); !errors.As(err, &permanent) || permanent.Status != 409 {
		t.Fatal(err)
	}
	if recv.Count() != 1 {
		t.Fatal("conflict replaced/duplicated durable record")
	}
}
func TestIdentityScopeAndApprovedKinds(t *testing.T) {
	cfg, r := fixture(t)
	dir := t.TempDir() + "/receiver"
	recv, err := receiver.Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	s := server(t, recv)
	if err = sink(t, s, "outsider").Send(context.Background(), r); !errors.Is(err, transfer.ErrAuthorization) {
		t.Fatal(err)
	}
	client := sink(t, s, "transfer-adapter")
	if err = client.Send(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Topic = strings.Replace(r.Topic, "/acme/", "/other/", 1)
	r.Digest = transfer.Digest(r.Topic, r.Payload)
	var p *transfer.PermanentError
	if err = client.Send(context.Background(), r); !errors.As(err, &p) || p.Status != 422 {
		t.Fatal(err)
	}
	if recv.Count() != 1 {
		t.Fatal("unapproved scope reached storage")
	}
	for _, kind := range []string{"metadata", "status"} {
		raw := cfg.MetadataBytes
		if kind == "status" {
			raw, err = cfg.Status("heartbeat", sample.UUID(), sample.UUID(), nil, false)
			if err != nil {
				t.Fatal(err)
			}
		}
		var obj map[string]any
		json.Unmarshal(raw, &obj)
		topic := cfg.Prefix + kind
		if err = client.Send(context.Background(), transfer.Record{Topic: topic, Payload: raw, ID: obj["message_id"].(string), Digest: transfer.Digest(topic, raw)}); err != nil {
			t.Fatal(kind, err)
		}
	}
	if recv.Count() != 3 {
		t.Fatal("missing approved kinds")
	}
	// Trust alone is insufficient: absence of the client certificate fails TLS.
	noClient := credentials(t, "transfer-adapter", false)
	noClient.Certificates = nil
	h := &http.Client{Transport: &http.Transport{TLSClientConfig: noClient}, Timeout: time.Second}
	if _, err = h.Get(s.URL); err == nil {
		t.Fatal("unauthenticated TLS accepted")
	}
	h.CloseIdleConnections()
}
func TestUntrustedReceiptsCannotRetire(t *testing.T) {
	_, r := fixture(t)
	good := transfer.Receipt{Profile: transfer.Profile, Enterprise: "acme", ID: r.ID, Digest: r.Digest, ReceiptID: sample.UUID(), State: "durable", Accepted: time.Now().UTC()}
	raw, _ := json.Marshal(good)
	if err := transfer.CheckReceipt(raw, "acme", r); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"profile", "enterprise", "message_id", "payload_digest", "receipt_id", "state", "accepted_at"} {
		t.Run(field, func(t *testing.T) {
			var obj map[string]any
			json.Unmarshal(raw, &obj)
			obj[field] = "wrong"
			body, _ := json.Marshal(obj)
			s := server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(201)
				w.Write(body)
			}))
			if err := sink(t, s, "transfer-adapter").Send(context.Background(), r); !errors.Is(err, transfer.ErrReceipt) {
				t.Fatal(err)
			}
		})
	}
	for _, body := range []string{string(raw[:len(raw)-1]) + `,"state":"durable"}`, string(raw[:len(raw)-1]) + `,"extra":1}`, strings.Repeat(" ", 4097), `null`, `[]`} {
		if err := transfer.CheckReceipt([]byte(body), "acme", r); !errors.Is(err, transfer.ErrReceipt) {
			t.Fatal("invalid receipt accepted", body)
		}
	}
	for _, status := range []int{202, 204, 429, 503, 401, 403, 400, 409, 413, 422, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "99999")
				w.WriteHeader(status)
				w.Write(raw)
			}))
			err := sink(t, s, "transfer-adapter").Send(context.Background(), r)
			if err == nil {
				t.Fatal("non-receipt status accepted")
			}
			if status == 429 {
				var hint *transfer.RetryError
				if !errors.As(err, &hint) || hint.After != 300*time.Second {
					t.Fatal(err)
				}
			}
		})
	}
	if _, err := transfer.NewSink("http://localhost/v1/ingest/observations", "acme", credentials(t, "transfer-adapter", false)); err == nil {
		t.Fatal("cleartext accepted")
	}
}

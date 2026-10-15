// Package cloudreceiver is a bounded, single-node test fixture, not Zeus.
// A file+directory fsync is its declared acceptance boundary. It never evicts
// receipts automatically: at capacity it rejects new requests with 503.
package cloudreceiver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
	"github.com/openump/Hades/internal/transfer"
	"github.com/openump/Hades/internal/transport"
)

type Entry struct {
	Record  transfer.Record  `json:"record"`
	Receipt transfer.Receipt `json:"receipt"`
}
type Receiver struct {
	mu    sync.Mutex
	cfg   *sample.Config
	store *storage.Store
}

func Open(cfg *sample.Config, dir string) (*Receiver, error) {
	s, err := storage.Open(dir, 100000, 256*1024*1024, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	r := &Receiver{cfg: cfg, store: s}
	for id, raw := range s.Records {
		var e Entry
		if err = json.Unmarshal(raw, &e); err == nil {
			var payload map[string]any
			payload, err = transfer.Validate(cfg, e.Record.Topic, e.Record.Payload)
			receipt, _ := json.Marshal(e.Receipt)
			if err == nil && (payload["message_id"] != e.Record.ID || id != transfer.Enterprise(cfg)+"/"+e.Record.ID || e.Record.Digest != transfer.Digest(e.Record.Topic, e.Record.Payload) || transfer.CheckReceipt(receipt, transfer.Enterprise(cfg), e.Record) != nil) {
				err = fmt.Errorf("invalid receiver record")
			}
		}
		if err != nil {
			s.Close()
			return nil, err
		}
	}
	return r, nil
}
func (r *Receiver) Close() error { r.mu.Lock(); defer r.mu.Unlock(); return r.store.Close() }
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// This fixture binds the single verified certificate identity to cfg scope.
	// A payload/header can never grant a second enterprise, site or topic.
	if transport.Peer(req) != "transfer-adapter" {
		http.Error(w, "forbidden", 403)
		return
	}
	if req.Method != http.MethodPost || req.URL.Path != "/v1/ingest/observations" || req.URL.RawQuery != "" {
		http.Error(w, "not found", 404)
		return
	}
	topic := req.Header.Get("X-OpenUMP-Topic")
	if len(topic) > 512 || req.Header.Get("X-OpenUMP-Profile") != transfer.Profile || req.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "invalid profile", 400)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, req.Body, contracts.MaxPayloadBytes))
	if err != nil {
		http.Error(w, "invalid body", 413)
		return
	}
	p, err := transfer.Validate(r.cfg, topic, raw)
	if err != nil {
		http.Error(w, "invalid context", 422)
		return
	}
	hash := transfer.Digest(topic, raw)
	if req.Header.Get("X-OpenUMP-Digest") != hash {
		http.Error(w, "invalid digest", 400)
		return
	}
	id := p["message_id"].(string)
	enterprise := transfer.Enterprise(r.cfg)
	key := enterprise + "/" + id
	r.mu.Lock()
	defer r.mu.Unlock()
	var entry Entry
	status := 201
	if saved, ok := r.store.Records[key]; ok {
		if err = json.Unmarshal(saved, &entry); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		if entry.Record.Digest != hash {
			http.Error(w, "identity conflict", 409)
			return
		}
		status = 200
	} else {
		entry = Entry{Record: transfer.Record{Topic: topic, Payload: raw, ID: id, Digest: hash, State: "durable", Accepted: time.Now().UTC()}, Receipt: transfer.Receipt{Profile: transfer.Profile, Enterprise: enterprise, ID: id, Digest: hash, ReceiptID: sample.UUID(), State: "durable", Accepted: time.Now().UTC()}}
		encoded, _ := json.Marshal(entry)
		if err = r.store.Put(key, encoded); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(entry.Receipt)
}

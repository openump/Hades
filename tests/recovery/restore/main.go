// Fixture seeding and verification only. Both deployed adapters remain unchanged.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/openump/Hades/internal/history"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/site"
	"github.com/openump/Hades/internal/transfer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	action := flag.String("action", "seed", "seed or verify")
	hdir := flag.String("history", "", "private history store")
	tdir := flag.String("transfer", "", "private transfer store")
	ledger := flag.String("ledger", "", "external source ledger")
	snapshot := flag.String("snapshot", "", "cold fixture directory")
	archive := flag.String("archive", "", "fixture archive")
	flag.Parse()
	if *action == "archive" {
		digest, err := site.Archive(*snapshot, *archive)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"sha256": digest})
	}
	cfg, err := sample.Load(".", "examples/site/metadata.json")
	if err != nil {
		return err
	}
	var raw []byte
	if *action == "seed" {
		raw, err = cfg.Observation("normal", 1, sample.UUID(), time.Now().Add(-time.Second))
		if err != nil {
			return err
		}
		if err = os.WriteFile(*ledger, raw, 0600); err != nil {
			return err
		}
	} else {
		raw, err = os.ReadFile(*ledger)
		if err != nil {
			return err
		}
	}
	h, err := history.Open(cfg, *hdir)
	if err != nil {
		return err
	}
	defer h.Close()
	q, err := transfer.Open(cfg, *tdir, 100000, 256*1024*1024)
	if err != nil {
		return err
	}
	defer q.Close()
	topic := cfg.Prefix + "telemetry"
	if *action == "seed" {
		if _, err = h.Intake(topic, raw); err != nil {
			return err
		}
		return q.Intake(topic, raw)
	}
	if h.Stats()["pending"] != 1 || q.Stats()["pending"] != 1 {
		return fmt.Errorf("restored pending ownership mismatch: %v %v", h.Stats(), q.Stats())
	}
	if state, err := h.Intake(topic, raw); err != nil || state != "duplicate" {
		return fmt.Errorf("history original identity missing: %v", err)
	}
	count := 0
	if err = q.Drain(context.Background(), func(_ context.Context, r transfer.Record) error {
		count++
		if string(r.Payload) != string(raw) || r.Topic != topic || r.Digest != transfer.Digest(topic, raw) {
			return fmt.Errorf("restored transfer identity/payload changed")
		}
		return nil
	}); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("restored replay count mismatch")
	}
	var p map[string]any
	json.Unmarshal(raw, &p)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"restored_message_id": p["message_id"], "original_observed_at": p["observed_at"], "history_pending": 1, "transfer_replayed": 1, "lost": 0})
}

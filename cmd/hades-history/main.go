package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/history"
	"github.com/openump/Hades/internal/operations"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
	"github.com/openump/Hades/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	broker := flag.String("broker", "mqtt://127.0.0.1:1883", "local MQTT")
	address := flag.String("historian", "http://127.0.0.1:8428", "local VictoriaMetrics")
	root := flag.String("root", ".", "repository root")
	config := flag.String("config", "examples/site/metadata.json", "approved metadata snapshot")
	intake := flag.String("intake", "runtime/history-intake", "private synced intake")
	quarantine := flag.String("quarantine", "runtime/history-quarantine", "private diagnostic store")
	resume := flag.String("resume-id", "", "resume one suspended immutable message before running")
	batchRecords := flag.Int("batch-records", 64, "maximum records per historian request (1..256)")
	batchBytes := flag.Int("batch-bytes", 1048576, "maximum historian request bytes (65536..1048576)")
	credentials := transport.Flags()
	logPath := operations.LogFlag()
	flag.Parse()
	closeLog, err := operations.ConfigureLog(*logPath)
	if err != nil {
		return err
	}
	defer closeLog()
	cfg, err := sample.LoadContext(*root, *config)
	if err != nil {
		return err
	}
	cfg.TLS, err = transport.Client(*credentials)
	if err != nil {
		return err
	}
	adapter, err := history.Open(cfg, *intake)
	if err != nil {
		return err
	}
	defer adapter.Close()
	if err = adapter.ConfigureDrain(*batchRecords, *batchBytes); err != nil {
		return err
	}
	if *resume != "" {
		if err = adapter.Resume(*resume); err != nil {
			return err
		}
	}
	diagnostic, err := storage.OpenDiagnostics(*quarantine)
	if err != nil {
		return err
	}
	defer diagnostic.Close()
	adapter.Diagnostic = diagnostic.Write
	sink, err := history.NewSinkTLS(*address, cfg.TLS)
	if err != nil {
		return err
	}
	defer sink.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	subscription, err := adapter.Subscribe(ctx, *broker, diagnostic.Write)
	if err != nil {
		return err
	}
	defer subscription.Close()
	workerDone := make(chan error, 1)
	go func() {
		for {
			if err := adapter.Drain(ctx, sink.Write); err != nil {
				workerDone <- err
				return
			}
			select {
			case <-ctx.Done():
				workerDone <- ctx.Err()
				return
			case <-time.After(adapter.NextDrainDelay()):
			}
		}
	}()
	encoder := json.NewEncoder(os.Stdout)
	encoder.Encode(map[string]any{"type": "history_ready", "boundary": "synced_intake_then_best_effort_api_acceptance"})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			encoder.Encode(map[string]any{"type": "history_stats", "stats": adapter.Stats(), "quarantine": diagnostic.Stats()})
		case <-ctx.Done():
			subscription.Close()
			<-workerDone
			return nil
		case err := <-subscription.Errors:
			encoder.Encode(map[string]any{"type": "history_failure", "stats": adapter.Stats(), "quarantine": diagnostic.Stats()})
			stop()
			subscription.Close()
			<-workerDone
			return err
		case err := <-workerDone:
			subscription.Close()
			if err == context.Canceled || err == context.DeadlineExceeded {
				return nil
			}
			return err
		}
	}
}

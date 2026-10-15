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

	"github.com/openump/Hades/internal/operations"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
	"github.com/openump/Hades/internal/transfer"
	"github.com/openump/Hades/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	broker := flag.String("broker", "mqtt://127.0.0.1:1883", "local MQTT")
	address := flag.String("receiver", "", "approved HTTPS receipt endpoint")
	root := flag.String("root", ".", "repository root")
	config := flag.String("config", "examples/site/metadata.json", "approved metadata snapshot")
	intake := flag.String("intake", "runtime/transfer-intake", "private synced intake")
	quarantine := flag.String("quarantine", "runtime/transfer-quarantine", "private diagnostic store")
	resume := flag.String("resume-id", "", "resume one suspended immutable message before running")
	records := flag.Int("queue-records", 100000, "bounded transfer records including suspended")
	bytes := flag.Int("queue-bytes", 256*1024*1024, "bounded transfer bytes including record overhead")
	cloudCredentials := &transport.Paths{}
	flag.StringVar(&cloudCredentials.CA, "cloud-ca", "", "receiver trust CA")
	flag.StringVar(&cloudCredentials.Cert, "cloud-cert", "", "transfer identity certificate")
	flag.StringVar(&cloudCredentials.Key, "cloud-key", "", "transfer private key")
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
	adapter, err := transfer.Open(cfg, *intake, *records, *bytes)
	if err != nil {
		return err
	}
	defer adapter.Close()
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

	cloudTLS, err := transport.Client(*cloudCredentials)
	if err != nil {
		return err
	}
	sink, err := transfer.NewSink(*address, transfer.Enterprise(cfg), cloudTLS)
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
			if err := adapter.Drain(ctx, sink.Send); err != nil {
				if err == transfer.ErrAuthorization {
					log.Print(err)
					<-ctx.Done()
					workerDone <- ctx.Err()
					return
				}
				workerDone <- err
				return
			}
			select {
			case <-ctx.Done():
				workerDone <- ctx.Err()
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	encoder := json.NewEncoder(os.Stdout)
	encoder.Encode(map[string]any{"type": "transfer_ready", "boundary": "synced_intake_then_verified_durable_receipt"})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			encoder.Encode(map[string]any{"type": "transfer_stats", "stats": adapter.Stats(), "quarantine": diagnostic.Stats()})
		case <-ctx.Done():
			subscription.Close()
			<-workerDone
			return nil
		case err := <-subscription.Errors:
			encoder.Encode(map[string]any{"type": "transfer_failure", "stats": adapter.Stats(), "quarantine": diagnostic.Stats()})
			stop()
			subscription.Close()
			<-workerDone
			return err
		case err := <-workerDone:
			encoder.Encode(map[string]any{"type": "transfer_failure", "stats": adapter.Stats(), "quarantine": diagnostic.Stats()})
			subscription.Close()
			if err == context.Canceled || err == context.DeadlineExceeded {
				return nil
			}
			return err
		}
	}
}

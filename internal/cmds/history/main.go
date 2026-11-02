package history

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/history"
	"github.com/openump/Hades/internal/infer"
	"github.com/openump/Hades/internal/operations"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/storage"
	"github.com/openump/Hades/internal/transport"
)

// Main runs hades-history with its arguments (without the program name) and
// returns the process exit code. Flags parse on a set named after the
// command, so usage text and -h behave as the standalone binary did.
func Main(args []string) int {
	fs := flag.NewFlagSet("hades-history", flag.ExitOnError)
	if err := run(fs, args); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}
func run(fs *flag.FlagSet, args []string) error {
	broker := fs.String("broker", "mqtt://127.0.0.1:1883", "local MQTT")
	address := fs.String("historian", "http://127.0.0.1:8428", "local VictoriaMetrics")
	root := fs.String("root", ".", "repository root")
	config := fs.String("config", "examples/site/metadata.json", "approved metadata snapshot")
	intake := fs.String("intake", "runtime/history-intake", "private synced intake")
	quarantine := fs.String("quarantine", "runtime/history-quarantine", "private diagnostic store")
	resume := fs.String("resume-id", "", "resume one suspended immutable message before running")
	batchRecords := fs.Int("batch-records", 64, "maximum records per historian request (1..256)")
	batchBytes := fs.Int("batch-bytes", 1048576, "maximum historian request bytes (65536..1048576)")
	credentials := transport.FlagsOn(fs)
	derivedBundles := fs.String("derived-bundles", "", "opt-in: comma-separated verified model bundles whose soft-sensor outputs may be stored (Hades-043)")
	derivedKeys := fs.String("derived-keys", "", "model release public keyring for -derived-bundles")
	logPath := operations.LogFlagOn(fs)
	fs.Parse(args)
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
	derived, err := derivedDeclarations(cfg, *derivedBundles, *derivedKeys)
	if err != nil {
		return err
	}
	adapter, err := history.OpenWith(cfg, *intake, derived)
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

// derivedDeclarations verifies each bundle the way hades-infer does (manifest
// schema, Ed25519 signature, artifact digest) and returns its soft-sensor
// outputs. No bundles means derived ingestion stays off.
func derivedDeclarations(cfg *sample.Config, bundles, keys string) (contracts.DerivedDeclarations, error) {
	var dirs []string
	for _, d := range strings.Split(bundles, ",") {
		if d = strings.TrimSpace(d); d != "" {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		if keys != "" {
			return nil, fmt.Errorf("-derived-keys requires -derived-bundles")
		}
		return nil, nil
	}
	if keys == "" {
		return nil, fmt.Errorf("-derived-bundles requires -derived-keys")
	}
	if !cfg.Validator.Supports("derived-value") {
		return nil, fmt.Errorf("-derived-bundles needs the derived-value schema in the site schema bundle")
	}
	trust, err := actuator.LoadPublicKeys(keys)
	if err != nil {
		return nil, err
	}
	declared := contracts.DerivedDeclarations{}
	for _, dir := range dirs {
		bundle, err := infer.LoadBundle(dir, cfg.Validator, infer.Trust(trust))
		if err != nil {
			return nil, fmt.Errorf("derived bundle %s: %w", dir, err)
		}
		for key, d := range bundle.Manifest.Declarations() {
			declared[key] = d
		}
	}
	if len(declared) == 0 {
		return nil, fmt.Errorf("-derived-bundles declare no soft-sensor outputs")
	}
	devices := map[string]map[string]any{}
	for _, asset := range cfg.Assets() {
		if id, ok := asset.Metadata["asset_id"].(string); ok {
			devices[id], _ = asset.Metadata["measurements"].(map[string]any)
		}
	}
	if err := declared.CheckDeclarationsAgainstDevices(func(asset string) map[string]any { return devices[asset] }); err != nil {
		return nil, err
	}
	return declared, nil
}

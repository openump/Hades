package simulator

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
)

func run(fs *flag.FlagSet, args []string) error {
	broker := fs.String("broker", "mqtt://127.0.0.1:1883", "localhost MQTT endpoint")
	root := fs.String("root", ".", "repository root")
	config := fs.String("config", "examples/site/metadata.json", "approved sample metadata snapshot")
	count := fs.Int("count", 3, "number of synthetic observations (1–10000)")
	scenario := fs.String("scenario", "cycle", "normal, bad-quality, missing-reading, or cycle")
	timeout := fs.Duration("timeout", 60*time.Second, "total run deadline")
	interval := fs.Duration("interval", 0, "optional pause between observations (0: back to back)")
	credentials := transport.FlagsOn(fs)
	fs.Parse(args)
	if fs.NArg() != 0 || *count < 1 || *count > 10000 || *timeout <= 0 || *interval < 0 {
		return fmt.Errorf("invalid arguments")
	}
	if *scenario != "cycle" && *scenario != "normal" && *scenario != "bad-quality" && *scenario != "missing-reading" {
		return fmt.Errorf("unknown scenario")
	}
	cfg, err := sample.Load(*root, *config)
	if err != nil {
		return err
	}
	cfg.TLS, err = transport.Client(*credentials)
	if err != nil {
		return err
	}
	signalctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalctx, *timeout)
	defer cancel()
	client, err := sample.Connect(ctx, *broker, cfg, true)
	if err != nil {
		return err
	}
	defer client.Close()
	send := func(topic string, raw []byte, retained bool, expiry uint32) error {
		return sample.Send(ctx, raw, func(attempt context.Context, copy []byte) error {
			return client.Publish(attempt, topic, copy, retained, expiry)
		})
	}
	var status []byte
	var last time.Time
	for sequence := 0; sequence < *count; sequence++ {
		if sequence > 0 && *interval > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(*interval):
			}
		}
		selected := *scenario
		if selected == "cycle" {
			selected = []string{"normal", "bad-quality", "missing-reading"}[sequence%3]
		}
		observed := time.Now()
		raw, err := cfg.Observation(selected, sequence, client.BootID, observed)
		if err != nil {
			return err
		}
		if err := send(cfg.Prefix+"telemetry", raw, false, 0); err != nil {
			return err
		}
		last = observed
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "published", "scenario": selected, "payload": payload}); err != nil {
			return err
		}
		status, err = cfg.Status("heartbeat", client.BootID, client.Session(), &last, selected != "normal")
		if err != nil {
			return err
		}
		if err := send(cfg.Prefix+"status", status, true, cfg.Expiry()); err != nil {
			return err
		}
		if sequence+1 < *count {
			timer := time.NewTimer(cfg.Interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	status, err = cfg.Status("shutdown", client.BootID, client.Session(), &last, false)
	if err != nil {
		return err
	}
	return send(cfg.Prefix+"status", status, true, cfg.Expiry())
}

// Main runs hades-simulator with its arguments (without the program name) and
// returns the process exit code. Flags parse on a set named after the
// command, so usage text and -h behave as the standalone binary did.
func Main(args []string) int {
	fs := flag.NewFlagSet("hades-simulator", flag.ExitOnError)
	if err := run(fs, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

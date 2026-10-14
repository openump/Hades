// hades-gateway is a gated lab acquisition wrapper, not an approved OT pilot.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/gateway"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	lab := flag.Bool("lab", false, "explicitly enable the loopback-only fixture profile")
	binary := flag.String("telegraf", "", "explicit pinned Telegraf binary")
	source := flag.String("source", "tcp://127.0.0.1:1502", "lab Modbus source")
	ingress := flag.String("ingress", "https://127.0.0.1:8081/observations", "authenticated normalizer ingress")
	config := flag.String("config", "examples/site/modbus-lab.metadata.json", "approved local lab context")
	root := flag.String("root", ".", "repository root")
	count := flag.Int("count", 3, "observations before exit, 1..10000")
	credentials := transport.Flags()
	flag.Parse()
	if !*lab || *count < 1 || *count > 10000 {
		return fmt.Errorf("explicit -lab and bounded count required; site approval is pending")
	}
	tlsConfig, err := transport.Client(*credentials)
	if err != nil {
		return err
	}
	if tlsConfig == nil {
		return fmt.Errorf("gateway requires mTLS ingress credentials")
	}
	u, err := url.Parse(*ingress)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Path != "/observations" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("ingress must be a fixed HTTPS observations endpoint")
	}
	cfg, err := sample.LoadContext(*root, *config)
	if err != nil {
		return err
	}
	definition, ok := cfg.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any)
	if !ok || len(cfg.Metadata["measurements"].(map[string]any)) != 1 || definition["type"] != "number" || definition["unit"] != "Cel" || cfg.Metadata["acquisition"].(map[string]any)["protocol"] != "modbus-tcp" {
		return fmt.Errorf("unsupported lab metadata")
	}
	interval := time.Duration(sample.Num(definition["expected_interval_ms"])) * time.Millisecond
	if interval < time.Second || interval > time.Hour {
		return fmt.Errorf("lab poll interval must be 1s..1h")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	driver, err := gateway.OpenDriver(ctx, *binary, *source)
	if err != nil {
		return err
	}
	defer driver.Close()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil, ResponseHeaderTimeout: 3 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	boot := sample.UUID()
	encoder := json.NewEncoder(os.Stdout)
	for sequence := 0; sequence < *count; sequence++ {
		started := time.Now()
		value, observed, reason := driver.Read(ctx)
		if ctx.Err() != nil {
			return nil
		}
		raw, err := gateway.Envelope(cfg, value, reason, boot, sequence, observed)
		if err != nil {
			return err
		}
		// Bound the acquisition copy to exactly one pending envelope. No new device
		// read occurs until admission; a restart loses this unsaved observation.
		if err = gateway.Submit(ctx, client, *ingress, raw); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		encoder.Encode(map[string]any{"type": "gateway_admitted", "payload": json.RawMessage(raw), "boundary": "accepted_volatile"})
		if sequence+1 < *count {
			timer := time.NewTimer(max(time.Until(started.Add(interval)), 0))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
	return nil
}

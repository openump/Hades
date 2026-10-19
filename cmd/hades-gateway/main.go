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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/gateway"
	"github.com/openump/Hades/internal/operations"
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
	logPath := operations.LogFlag()
	flag.Parse()
	closeLog, err := operations.ConfigureLog(*logPath)
	if err != nil {
		return err
	}
	defer closeLog()
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
	if _, ok := cfg.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any); !ok {
		return fmt.Errorf("unsupported lab metadata")
	}
	interval := cfg.Interval
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	statusEncoder := json.NewEncoder(os.Stdout)
	gatewayID := cfg.Metadata["acquisition"].(map[string]any)["gateway_id"].(string)
	opened, err := gateway.OpenConfiguredDriver(ctx, cfg, *source, gateway.DriverOptions{
		PollInterval: interval,
		ReadTimeout:  2 * time.Second,
		GatewayID:    gatewayID,
		StatusReporter: func(event gateway.ProducerStatusEvent) {
			_ = statusEncoder.Encode(map[string]any{
				"type":        "producer_status",
				"gateway_id":  event.GatewayID,
				"endpoint":    event.Endpoint,
				"state":       event.State,
				"reason":      event.Reason,
				"reported_at": event.At.UTC().Format(sample.Timestamp),
			})
		},
	}, gateway.FactoryOptions{TelegrafBinary: *binary})
	if err != nil {
		return err
	}
	driver, ok := opened.(*gateway.Driver)
	if !ok {
		opened.Close()
		return fmt.Errorf("configured driver for modbus-tcp did not return a Telegraf implementation")
	}
	defer driver.Close()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil, ResponseHeaderTimeout: 3 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var acquired, failures, admitted, retries, pending, latencyUS atomic.Uint64
	healthDone := make(chan struct{})
	encoder := json.NewEncoder(os.Stdout)
	defer close(healthDone)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-healthDone:
				return
			case <-ticker.C:
				event, _ := json.Marshal(map[string]any{"type": "gateway_health", "acquired": acquired.Load(), "acquisition_failures": failures.Load(), "admitted": admitted.Load(), "ingress_retries": retries.Load(), "pending_records": pending.Load(), "pending_limit_records": 1, "acquisition_us_sum": latencyUS.Load(), "clock_freshness": "unknown", "restart_loss_count": "unknown"})
				log.Print(string(event))
			}
		}
	}()
	boot := sample.UUID()
	for sequence := 0; sequence < *count; sequence++ {
		started := time.Now()
		value, observed, reason := driver.Read(ctx)
		// Driver startup varies. Space reads from completion so a fast subsequent
		// launch cannot exceed the approved maximum device polling frequency.
		nextPoll := time.Now().Add(interval)
		acquired.Add(1)
		latencyUS.Add(uint64(time.Since(started).Microseconds()))
		if value == nil {
			failures.Add(1)
		}
		if ctx.Err() != nil {
			return nil
		}
		raw, err := gateway.Envelope(cfg, value, reason, boot, sequence, observed)
		if err != nil {
			return err
		}
		// Bound the acquisition copy to exactly one pending envelope. No new device
		// read occurs until admission; a restart loses this unsaved observation.
		pending.Store(1)
		if err = gateway.SubmitObserved(ctx, client, *ingress, raw, func() { retries.Add(1) }); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		pending.Store(0)
		admitted.Add(1)
		encoder.Encode(map[string]any{"type": "gateway_admitted", "payload": json.RawMessage(raw), "boundary": "accepted_volatile"})
		if sequence+1 < *count {
			timer := time.NewTimer(max(time.Until(nextPoll), 0))
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

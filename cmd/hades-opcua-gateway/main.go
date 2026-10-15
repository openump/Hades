// hades-opcua-gateway acquires only the fixed local software-pilot nodes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/awcullen/opcua/ua"
	"github.com/openump/Hades/internal/gateway"
	"github.com/openump/Hades/internal/opcuapilot"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	endpoint := flag.String("endpoint", "opc.tcp://127.0.0.1:4840", "fixed loopback software pilot")
	pki := flag.String("opcua-pki", "", "private pinned application certificates")
	config := flag.String("config", "examples/site/opcua-mock.metadata.json", "approved OPC UA mock context")
	root := flag.String("root", ".", "schema root")
	ingress := flag.String("ingress", "https://127.0.0.1:8081/observations", "mTLS normalizer ingress")
	count := flag.Int("count", 4, "bounded polls, 1..1000")
	credentials := transport.Flags()
	flag.Parse()
	if *count < 1 || *count > 1000 {
		return fmt.Errorf("poll count must be 1..1000")
	}
	if err := opcuapilot.ValidateEndpoint(*endpoint); err != nil {
		return err
	}
	cfg, err := sample.LoadContext(*root, *config)
	if err != nil {
		return err
	}
	if cfg.Metadata["acquisition"].(map[string]any)["protocol"] != "opc-ua" || cfg.Interval != time.Second {
		return fmt.Errorf("requires approved one-second OPC UA mock context")
	}
	if err := sample.CheckOPCUAMock(cfg); err != nil {
		return err
	}
	tls, err := transport.Client(*credentials)
	if err != nil {
		return err
	}
	if tls == nil {
		return fmt.Errorf("mTLS ingress required")
	}
	u, err := url.Parse(*ingress)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "/observations" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("fixed HTTPS observations ingress required")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("software pilot ingress must use a loopback IP")
	}
	sink := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tls, Proxy: nil, ResponseHeaderTimeout: 3 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer sink.CloseIdleConnections()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, deadlineStop := context.WithTimeout(ctx, 15*time.Minute)
	defer deadlineStop()
	var reader *opcuapilot.Reader
	defer func() {
		if reader != nil {
			reader.Close()
		}
	}()
	var acquired, failures, admitted, retries, badReadings atomic.Uint64
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		health := json.NewEncoder(os.Stdout)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if health.Encode(map[string]any{"type": "opcua_gateway_health", "polls": acquired.Load(), "acquisition_failures": failures.Load(), "bad_readings": badReadings.Load(), "admitted": admitted.Load(), "ingress_retries": retries.Load(), "volatile_poll_limit_records": 3}) != nil {
					stop()
					return
				}
			}
		}
	}()
	boot := sample.UUID()
	sequence := 0
	output := json.NewEncoder(os.Stdout)
	fmt.Fprintln(os.Stderr, "opcua_gateway_ready")
	for poll := 0; poll < *count; poll++ {
		connect, cancel := context.WithTimeout(ctx, 4*time.Second)
		if reader == nil {
			reader, err = opcuapilot.Dial(connect, *endpoint, *pki, "client")
		}
		cancel()
		var values []ua.DataValue
		if err == nil {
			read, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			values, err = reader.Read(read)
			cancel()
		}
		acquired.Add(1)
		completed := time.Now().UTC()
		next := completed.Add(time.Second)
		if ctx.Err() != nil {
			return cancelled(ctx)
		}
		if err != nil {
			failures.Add(1)
			if reader != nil {
				reader.Close()
				reader = nil
			}
			values = make([]ua.DataValue, len(opcuapilot.Signals))
			for i := range values {
				values[i] = ua.NewDataValue(nil, ua.BadCommunicationError, time.Time{}, 0, time.Time{}, 0)
			}
		}
		for i, signal := range opcuapilot.Signals {
			reading, stamp, origin, ext := opcuapilot.MapValue(signal, values[i], completed)
			if reading["quality"] == "bad" {
				badReadings.Add(1)
			}
			ext["opcua.namespace_uri"] = opcuapilot.NamespaceURI
			ext["opcua.status_origin"] = "server"
			if reader != nil {
				ext["opcua.namespace_index"] = reader.Namespace
			} else {
				ext["opcua.namespace_index"] = nil
			}
			if err != nil {
				ext["opcua.status_origin"] = "gateway"
				ext["opcua.transport_error"] = "connection_failed"
				if errors.Is(err, context.DeadlineExceeded) {
					ext["opcua.transport_error"] = "timeout"
				}
			}
			payload := map[string]any{"schema_version": "1.0", "asset_id": cfg.Metadata["asset_id"], "metadata_revision": cfg.Metadata["metadata_revision"], "message_id": sample.UUID(), "observed_at": stamp.UTC().Format(sample.Timestamp), "ingested_at": time.Now().UTC().Format(sample.Timestamp), "source": map[string]any{"gateway_id": cfg.Metadata["acquisition"].(map[string]any)["gateway_id"], "protocol": "opc-ua", "boot_id": boot, "sequence": sequence, "clock_origin": origin, "clock_uncertainty_ms": nil}, "signals": map[string]any{signal.Name: reading}, "extensions": ext}
			raw, _ := json.Marshal(payload)
			sequence++
			if err := gateway.SubmitObserved(ctx, sink, *ingress, raw, func() { retries.Add(1) }); err != nil {
				if ctx.Err() != nil {
					return cancelled(ctx)
				}
				return err
			}
			admitted.Add(1)
			if err := output.Encode(map[string]any{"type": "gateway_admitted", "payload": payload, "boundary": "accepted_volatile"}); err != nil {
				return err
			}
		}
		if poll+1 < *count {
			timer := time.NewTimer(max(0, time.Until(next)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return cancelled(ctx)
			case <-timer.C:
			}
		}
	}
	return nil
}

func cancelled(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	return nil
}

// hades-normalizer runs the localhost reference ingress and MQTT publisher.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/openump/Hades/internal/normalize"
	"github.com/openump/Hades/internal/operations"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	broker := flag.String("broker", "mqtt://127.0.0.1:1883", "local MQTT address")
	listen := flag.String("listen", "127.0.0.1:8081", "loopback HTTP ingress")
	root := flag.String("root", ".", "repository root")
	config := flag.String("config", "examples/site/metadata.json", "approved development metadata (comma-separated for multi-asset catalog)")
	profile := flag.String("signal-profile", "temperature", "temperature, approved-numeric (simulator) or opcua-mock")
	mapping := flag.String("mapping", "config/normalization/temperature.blobl", "trusted reading mapping")
	quarantine := flag.String("quarantine", "runtime/quarantine", "private bounded quarantine")
	records := flag.Int("queue-records", 4096, "bounded in-flight publication request limit")
	timeout := flag.Duration("timeout", 0, "optional development run duration")
	ingressWriters := flag.String("ingress-writers", "gateway-press-04", "comma-separated mTLS writer identities (CN/SAN, optional @asset_id scope)")
	ingressReaders := flag.String("ingress-readers", "admin", "comma-separated mTLS reader identities for /health")
	serverCredentials := &transport.Paths{}
	flag.StringVar(&serverCredentials.CA, "ingress-ca", "", "ingress client CA")
	flag.StringVar(&serverCredentials.Cert, "ingress-cert", "", "ingress server certificate")
	flag.StringVar(&serverCredentials.Key, "ingress-key", "", "ingress server key")
	credentials := transport.Flags()
	logPath := operations.LogFlag()
	flag.Parse()
	closeLog, err := operations.ConfigureLog(*logPath)
	if err != nil {
		return err
	}
	defer closeLog()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return fmt.Errorf("ingress must bind a loopback IP")
	}
	policy, err := transport.NewIngressPolicy(transport.ParseCSVIdentities(*ingressWriters), transport.ParseCSVIdentities(*ingressReaders))
	if err != nil {
		return err
	}
	cfg, err := sample.LoadContext(*root, *config)
	if err == nil && *profile == "temperature" {
		cfg, err = sample.LoadReadingContext(*root, *config)
	}
	if err != nil {
		return err
	}
	cfg.TLS, err = transport.Client(*credentials)
	if err != nil {
		return err
	}
	if cfg.TLS != nil && *serverCredentials == (transport.Paths{}) {
		return fmt.Errorf("secure broker requires mTLS ingress credentials")
	}
	if _, err = transport.BrokerURL(*broker, cfg.TLS); err != nil {
		return err
	}
	var engine *normalize.Engine
	switch *profile {
	case "temperature":
		raw, readErr := os.ReadFile(*mapping)
		if readErr != nil {
			return readErr
		}
		if len(raw) > 65536 {
			return fmt.Errorf("mapping too large")
		}
		engine, err = normalize.New(cfg, string(raw))
	case "opcua-mock":
		engine, err = normalize.NewOPCUAMock(cfg)
	case "approved-numeric":
		engine, err = normalize.NewApprovedNumeric(cfg)
	default:
		return fmt.Errorf("unknown signal profile")
	}
	if err != nil {
		return err
	}
	ingress, err := normalize.NewServer(engine, *quarantine, *records, 16*1024*1024)
	if err != nil {
		return err
	}
	ingress.IngressPolicy = policy
	defer ingress.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	var client *sample.Client // Owned only by the publication worker.
	defer func() {
		if client != nil {
			client.Close()
		}
	}()
	server := &http.Server{Addr: *listen, Handler: ingress, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	errors := make(chan error, 2)
	workerDone := make(chan struct{})
	if *serverCredentials != (transport.Paths{}) {
		tlsConfig, err := transport.Server(*serverCredentials)
		if err != nil {
			listener.Close()
			return err
		}
		server.TLSConfig = tlsConfig
		server.Handler = policy.Wrap(ingress)
		go func() { errors <- server.ServeTLS(listener, "", "") }()
	} else {
		go func() { errors <- server.Serve(listener) }()
	}
	go func() {
		defer close(workerDone)
		errors <- ingress.Run(ctx, func(c context.Context, p []byte) error {
			topic := cfg.Prefix + "telemetry"
			if obj, err := cfg.Validator.Validate("telemetry", p); err == nil {
				if assetCfg, err := cfg.ResolveAsset(obj["asset_id"], obj["metadata_revision"]); err == nil {
					topic = assetCfg.Prefix + "telemetry"
				}
			}
			if client == nil {
				var err error
				client, err = sample.Connect(c, *broker, cfg, true)
				if err != nil {
					return err
				}
			}
			return client.Publish(c, topic, p, false, 0)
		})
	}()
	log.Printf("normalizer ready on %s; success requires MQTT PUBACK", *listen)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	output := json.NewEncoder(os.Stdout)
runLoop:
	for {
		select {
		case <-ctx.Done():
			break runLoop
		case err = <-errors:
			stop()
			break runLoop
		case <-ticker.C:
			if err = output.Encode(map[string]any{"type": "normalizer_stats", "stats": ingress.Stats()}); err != nil {
				stop()
				break runLoop
			}
		}
	}
	// Wait for handlers and the worker before releasing the store lock.
	shutdownctx, done := context.WithTimeout(context.Background(), 6*time.Second)
	defer done()
	server.Shutdown(shutdownctx)
	stop()
	// Run completion can be observed separately from the HTTP Serve completion.
	select {
	case <-workerDone:
	case <-shutdownctx.Done():
		return shutdownctx.Err()
	}
	if err == http.ErrServerClosed || err == context.Canceled || err == context.DeadlineExceeded {
		return nil
	}
	return err
}

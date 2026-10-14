// hades-normalizer runs the localhost reference ingress and MQTT publisher.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/openump/Hades/internal/normalize"
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
	config := flag.String("config", "examples/site/metadata.json", "approved development metadata")
	mapping := flag.String("mapping", "config/normalization/temperature.blobl", "trusted reading mapping")
	quarantine := flag.String("quarantine", "runtime/quarantine", "private bounded quarantine")
	records := flag.Int("queue-records", 4096, "volatile queue record limit")
	timeout := flag.Duration("timeout", 0, "optional development run duration")
	serverCredentials := &transport.Paths{}
	flag.StringVar(&serverCredentials.CA, "ingress-ca", "", "ingress client CA")
	flag.StringVar(&serverCredentials.Cert, "ingress-cert", "", "ingress server certificate")
	flag.StringVar(&serverCredentials.Key, "ingress-key", "", "ingress server key")
	credentials := transport.Flags()
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return fmt.Errorf("ingress must bind a loopback IP")
	}
	cfg, err := sample.Load(*root, *config)
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
	raw, err := os.ReadFile(*mapping)
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return fmt.Errorf("mapping too large")
	}
	engine, err := normalize.New(cfg, string(raw))
	if err != nil {
		return err
	}
	ingress, err := normalize.NewServer(engine, *quarantine, *records, 16*1024*1024)
	if err != nil {
		return err
	}
	defer ingress.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	connectctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := sample.Connect(connectctx, *broker, cfg, true)
	if err != nil {
		return err
	}
	defer client.Close()
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
		server.Handler = transport.Ingress(ingress)
		go func() { errors <- server.ServeTLS(listener, "", "") }()
	} else {
		go func() { errors <- server.Serve(listener) }()
	}
	go func() {
		defer close(workerDone)
		errors <- ingress.Run(ctx, func(c context.Context, p []byte) error { return client.Publish(c, cfg.Prefix+"telemetry", p, false, 0) })
	}()
	log.Printf("normalizer ready on %s; accepted work is volatile", *listen)
	select {
	case <-ctx.Done():
	case err = <-errors:
		stop()
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

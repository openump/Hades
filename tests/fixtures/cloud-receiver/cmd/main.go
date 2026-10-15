// Local verification fixture only. No production cloud deployment defaults.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
	receiver "github.com/openump/Hades/tests/fixtures/cloud-receiver"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("listen", "127.0.0.1:18443", "loopback fixture listener")
	store := flag.String("store", "runtime/test-receiver", "private synced receipt records")
	config := flag.String("config", "examples/site/metadata.json", "approved fixture metadata")
	fault := flag.String("fault-file", "", "local test-only mode: offline, receipt-loss, normal")
	credentials := transport.Flags()
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("fixture must bind literal loopback")
	}
	cfg, err := sample.LoadContext(".", *config)
	if err != nil {
		return err
	}
	tls, err := transport.Server(*credentials)
	if err != nil {
		return err
	}
	r, err := receiver.Open(cfg, *store)
	if err != nil {
		return err
	}
	defer r.Close()
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mode := []byte("normal")
		if *fault != "" {
			var err error
			mode, err = os.ReadFile(*fault)
			if err != nil {
				http.Error(w, "fixture fault config unavailable", 503)
				return
			}
		}
		switch string(mode) {
		case "normal":
			r.ServeHTTP(w, req)
		case "offline":
			http.Error(w, "fixture cloud outage", 503)
		case "receipt-loss":
			recorded := httptest.NewRecorder()
			r.ServeHTTP(recorded, req)
			if recorded.Code == 200 || recorded.Code == 201 {
				http.Error(w, "fixture lost receipt after durable commit", 503)
			} else {
				for k, v := range recorded.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(recorded.Code)
				w.Write(recorded.Body.Bytes())
			}
		default:
			http.Error(w, "unknown fixture fault mode", 503)
		}
	})
	server := &http.Server{Addr: *addr, Handler: handler, TLSConfig: tls, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServeTLS("", "") }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

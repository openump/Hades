package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/historyproxy"
	"github.com/openump/Hades/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:8429", "HTTPS listener")
	backend := flag.String("backend", "http://127.0.0.1:8428", "trusted historian on isolated network")
	credentials := transport.Flags()
	flag.Parse()
	tlsConfig, err := transport.Server(*credentials)
	if err != nil {
		return err
	}
	target, err := url.Parse(*backend)
	if err != nil || target.Scheme != "http" || target.User != nil || target.Hostname() == "" || target.Path != "" || target.RawQuery != "" {
		return fmt.Errorf("invalid fixed historian backend")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) { http.Error(w, "historian unavailable", 502) }
	server := &http.Server{Addr: *listen, TLSConfig: tlsConfig, Handler: historyproxy.Guard(proxy), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errors := make(chan error, 1)
	go func() { errors <- server.ListenAndServeTLS("", "") }()
	select {
	case err = <-errors:
	case <-ctx.Done():
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		return server.Shutdown(shutdown)
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

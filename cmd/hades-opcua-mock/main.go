//go:build opcua_mock

package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/openump/Hades/internal/opcuamock"
	"github.com/openump/Hades/internal/opcuapilot"
	"github.com/openump/Hades/internal/operations"
	"os"
	"os/signal"
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
	action := flag.String("action", "serve", "init or serve")
	pki := flag.String("pki", "", "private OPC UA identity directory")
	endpoint := flag.String("endpoint", "opc.tcp://127.0.0.1:4840", "loopback endpoint")
	fault := flag.String("fault-file", "", "local deterministic fault mode")
	ledger := flag.String("ledger", "", "private bounded source ledger")
	pad := flag.Bool("namespace-padding", false, "insert namespace before pilot (restart test)")
	duration := flag.Duration("timeout", 15*time.Minute, "bounded mock lifetime (up to 15m)")
	flag.Parse()
	if *action == "init" {
		return opcuapilot.InitPKI(*pki)
	}
	if *action != "serve" || *duration <= 0 || *duration > 15*time.Minute || *ledger == "" {
		return fmt.Errorf("invalid mock arguments")
	}
	logs, err := operations.OpenLog(*ledger, 5*1024*1024)
	if err != nil {
		return err
	}
	defer logs.Close()
	srv, err := opcuamock.New(*endpoint, *pki, *fault, logs, *pad)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		srv.Close()
		select {
		case <-done:
			return nil
		case <-time.After(3 * time.Second):
			return fmt.Errorf("mock shutdown timed out")
		}
	}
}

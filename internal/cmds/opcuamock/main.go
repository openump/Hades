//go:build opcua_mock

package opcuamock

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

// Main runs hades-opcua-mock with its arguments (without the program name) and
// returns the process exit code. Flags parse on a set named after the
// command, so usage text and -h behave as the standalone binary did.
func Main(args []string) int {
	fs := flag.NewFlagSet("hades-opcua-mock", flag.ExitOnError)
	if err := run(fs, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
func run(fs *flag.FlagSet, args []string) error {
	action := fs.String("action", "serve", "init or serve")
	pki := fs.String("pki", "", "private OPC UA identity directory")
	endpoint := fs.String("endpoint", "opc.tcp://127.0.0.1:4840", "loopback endpoint")
	fault := fs.String("fault-file", "", "local deterministic fault mode")
	ledger := fs.String("ledger", "", "private bounded source ledger")
	pad := fs.Bool("namespace-padding", false, "insert namespace before pilot (restart test)")
	duration := fs.Duration("timeout", 15*time.Minute, "bounded mock lifetime (up to 15m)")
	fs.Parse(args)
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

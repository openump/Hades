// hades-ctl is the operator CLI for one Hades node. Its only command, top,
// is a read-only terminal dashboard over hades-inspector's loopback snapshot.
package ctl

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/tui"
)

// Main runs hades-ctl with its arguments (without the program name) and
// returns the process exit code.
func Main(args []string) int {
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "hades-ctl:", err)
		return 1
	}
	return 0
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "top" {
		return fmt.Errorf("usage: hades-ctl top [-inspector 127.0.0.1:18091] [-interval 1s] [-once]")
	}
	flags := flag.NewFlagSet("top", flag.ContinueOnError)
	inspector := flags.String("inspector", "127.0.0.1:18091", "hades-inspector loopback address")
	interval := flags.Duration("interval", time.Second, "refresh interval")
	once := flags.Bool("once", false, "print one full frame (all panes, no colour) and exit; for support bundles, CI and pipes")
	color := flags.Bool("color", false, "keep ANSI colour in -once output")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *interval < 100*time.Millisecond || *interval > time.Hour {
		return fmt.Errorf("-interval must be between 100ms and 1h")
	}
	endpoint := tui.SnapshotURL(*inspector)
	if err := loopback(endpoint); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	opts := tui.Options{Endpoint: endpoint, Interval: *interval}

	interactive := tui.IsTerminal(int(os.Stdin.Fd())) && tui.IsTerminal(int(os.Stdout.Fd()))
	if *once || !interactive {
		if !*once {
			fmt.Fprintln(os.Stderr, "hades-ctl: no terminal, printing one frame (use ssh -t for the live dashboard)")
		}
		s := tui.TakeOnce(ctx, opts, 500*time.Millisecond)
		fmt.Print(tui.Once(s, time.Now(), *color))
		if s.Err != nil {
			return fmt.Errorf("daemon unreachable: %w", s.Err)
		}
		return nil
	}

	term, err := tui.OpenTerminal(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	defer term.Restore()
	keys := make(chan string, 16)
	go tui.ReadKeys(ctx, os.Stdin, keys) // blocked in read until the next key; exits with the process
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	resize := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-winch:
				select {
				case resize <- struct{}{}:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	opts.Resize = resize
	return tui.Run(ctx, opts, keys, os.Stdout, term.Size)
}

// loopback refuses any non-loopback inspector: the console data never
// leaves the node, and SSH is the only remote path.
func loopback(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" {
		return fmt.Errorf("-inspector must be a loopback http address")
	}
	if ip := net.ParseIP(u.Hostname()); (ip == nil || !ip.IsLoopback()) && u.Hostname() != "localhost" {
		return fmt.Errorf("-inspector must be loopback; reach a remote node over SSH and run hades-ctl there")
	}
	return nil
}

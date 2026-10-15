package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/openump/Hades/internal/supervisor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	action := flag.String("action", "status", "run, status, digest, unit")
	path := flag.String("config", "", "private reviewed service JSON")
	expected := flag.String("digest", "", "expected definition SHA256")
	name := flag.String("name", "", "unit name without suffix")
	user := flag.String("user", "", "non-root service account")
	flag.Parse()
	cfg, err := supervisor.Load(*path)
	if err != nil {
		return err
	}
	switch *action {
	case "digest":
		if err = supervisor.Validate(cfg); err != nil {
			return err
		}
		fmt.Println(supervisor.Digest(cfg))
		return nil
	case "unit":
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		text, err := supervisor.Unit(*name, *user, binary, *path, supervisor.Digest(cfg), append(cfg.WritablePaths, cfg.State)...)
		if err != nil {
			return err
		}
		fmt.Print(text)
		return nil
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return supervisor.Run(ctx, *path, *expected)
	case "status":
		state, err := supervisor.Probe(context.Background(), cfg)
		if err != nil {
			return err
		}
		if !state.Ready {
			return fmt.Errorf("child running; application readiness not yet observed")
		}
		return json.NewEncoder(os.Stdout).Encode(state)
	default:
		return fmt.Errorf("unknown action")
	}
}

package service

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

// Main runs hades-service with its arguments (without the program name) and
// returns the process exit code. Flags parse on a set named after the
// command, so usage text and -h behave as the standalone binary did.
func Main(args []string) int {
	fs := flag.NewFlagSet("hades-service", flag.ExitOnError)
	if err := run(fs, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
func run(fs *flag.FlagSet, args []string) error {
	action := fs.String("action", "status", "run, status, digest, unit")
	path := fs.String("config", "", "private reviewed service JSON")
	expected := fs.String("digest", "", "expected definition SHA256")
	name := fs.String("name", "", "unit name without suffix")
	user := fs.String("user", "", "non-root service account")
	fs.Parse(args)
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
		if err = supervisor.Validate(cfg); err != nil {
			return err
		}
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

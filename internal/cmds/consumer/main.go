package consumer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
)

func run(fs *flag.FlagSet, args []string) error {
	broker := fs.String("broker", "mqtt://127.0.0.1:1883", "localhost MQTT endpoint")
	root := fs.String("root", ".", "repository root")
	config := fs.String("config", "examples/site/metadata.json", "approved sample metadata snapshot")
	count := fs.Int("count", 3, "unique telemetry observations before exit (1–10000)")
	timeout := fs.Duration("timeout", 60*time.Second, "total run deadline")
	credentials := transport.FlagsOn(fs)
	fs.Parse(args)
	if fs.NArg() != 0 || *count < 1 || *count > 10000 || *timeout <= 0 {
		return fmt.Errorf("invalid arguments")
	}
	cfg, err := sample.LoadContext(*root, *config)
	if err != nil {
		return err
	}
	cfg.TLS, err = transport.Client(*credentials)
	if err != nil {
		return err
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signals, *timeout)
	defer cancel()
	client, err := sample.Connect(ctx, *broker, cfg, false)
	if err != nil {
		return err
	}
	defer client.Close()
	output := json.NewEncoder(os.Stdout)
	if err := output.Encode(map[string]any{"type": "ready"}); err != nil {
		return err
	}
	seen := map[string][32]byte{}
	order := make([]string, 4096)
	cursor, total := 0, 0
	for total < *count {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-client.Errors:
			return err
		case err := <-client.Ready:
			if err != nil {
				return err
			}
			if err := output.Encode(map[string]any{"type": "ready", "reconnected": true}); err != nil {
				return err
			}
		case message := <-client.Messages:
			summary, err := cfg.Inspect(message.Topic, message.Payload, time.Now())
			if err != nil {
				return err
			}
			if summary["type"] == "telemetry" && message.Retained {
				return fmt.Errorf("telemetry must not be retained")
			}
			id := summary["message_id"].(string)
			digest := sha256.Sum256(append(append([]byte(message.Topic), 0), message.Payload...))
			prior, duplicate := seen[id]
			if duplicate && prior != digest {
				return fmt.Errorf("conflicting duplicate message")
			}
			if !duplicate {
				delete(seen, order[cursor])
				order[cursor] = id
				cursor = (cursor + 1) % len(order)
				seen[id] = digest
			}
			summary["duplicate"] = duplicate
			summary["topic"] = message.Topic
			if err := output.Encode(summary); err != nil {
				return err
			}
			if summary["type"] == "telemetry" && !duplicate {
				total++
			}
		}
	}
	return nil
}

// Main runs hades-consumer with its arguments (without the program name) and
// returns the process exit code. Flags parse on a set named after the
// command, so usage text and -h behave as the standalone binary did.
func Main(args []string) int {
	fs := flag.NewFlagSet("hades-consumer", flag.ExitOnError)
	if err := run(fs, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

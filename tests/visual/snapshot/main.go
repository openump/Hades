// Command uns-snapshot is a Hades-046 test helper: it reads one retained
// status and one telemetry message for an asset from a running broker and
// writes their exact payloads, the inputs `hades aas export -status
// -telemetry` takes. Both are validated against the catalog first.
//
//	uns-snapshot -broker mqtt://127.0.0.1:1883 -catalog site/catalog/metadata.json -out live
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openump/Hades/internal/sample"
)

func main() {
	broker := flag.String("broker", "mqtt://127.0.0.1:1883", "broker")
	root := flag.String("root", ".", "directory containing schemas/")
	catalog := flag.String("catalog", "", "metadata snapshot of the asset")
	out := flag.String("out", "", "directory for status.json and telemetry.json")
	timeout := flag.Duration("timeout", 30*time.Second, "deadline")
	flag.Parse()
	if err := run(*broker, *root, *catalog, *out, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "uns-snapshot:", err)
		os.Exit(1)
	}
}

func run(broker, root, catalog, out string, timeout time.Duration) error {
	if catalog == "" || out == "" {
		return fmt.Errorf("-catalog and -out are required")
	}
	cfg, err := sample.LoadContext(root, catalog)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := sample.Connect(ctx, broker, cfg, false)
	if err != nil {
		return err
	}
	defer client.Close()
	got := map[string][]byte{}
	var lines []string
	for len(got) < 2 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out with %d of 2 messages", len(got))
		case err := <-client.Errors:
			return err
		case <-client.Ready:
		case m := <-client.Messages:
			summary, err := cfg.Inspect(m.Topic, m.Payload, time.Now())
			if err != nil {
				return err
			}
			kind, _ := summary["type"].(string)
			if (kind != "status" && kind != "telemetry") || got[kind] != nil || (kind == "status" && !m.Retained) {
				continue
			}
			got[kind] = m.Payload
			lines = append(lines, fmt.Sprintf("%-9s %s  %s", kind, m.Topic, describe(summary)))
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for kind, raw := range got {
		if err := os.WriteFile(filepath.Join(out, kind+".json"), raw, 0o644); err != nil {
			return err
		}
	}
	sort.Strings(lines)
	fmt.Println(strings.Join(lines, "\n"))
	fmt.Printf("wrote %s and %s\n", filepath.Join(out, "status.json"), filepath.Join(out, "telemetry.json"))
	return nil
}

// describe prints what an operator recognizes: the producer and acquisition
// state of a status, the readings of a telemetry message.
func describe(s map[string]any) string {
	switch s["type"] {
	case "status":
		return fmt.Sprintf("producer=%v acquisition=%v", s["producer_state"], s["acquisition_state"])
	case "telemetry":
		var parts []string
		if readings, ok := s["measurements"].(map[string]any); ok {
			names := make([]string, 0, len(readings))
			for n := range readings {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				r, _ := readings[n].(map[string]any)
				parts = append(parts, fmt.Sprintf("%s=%v %v (%v)", n, r["value"], r["unit"], r["quality"]))
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

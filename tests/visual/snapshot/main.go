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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openump/Hades/internal/contracts"
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
			text, err := describe(summary)
			if err != nil {
				return fmt.Errorf("%s: %w", m.Topic, err)
			}
			got[kind] = m.Payload
			lines = append(lines, fmt.Sprintf("%-9s %s  %s", kind, m.Topic, text))
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
// state of a status; the value, unit, quality and freshness of each reading.
// Broker text is escaped (strconv.Quote), never written raw to the terminal.
func describe(s map[string]any) (string, error) {
	field := func(m map[string]any, k string) (string, error) {
		v, ok := m[k]
		if !ok || v == nil {
			return "", fmt.Errorf("summary lacks %s", k)
		}
		text := fmt.Sprint(v)
		if q := strconv.Quote(text); q[1:len(q)-1] != text || strings.ContainsFunc(text, contracts.IsBidiControl) {
			return strconv.QuoteToASCII(text), nil
		}
		return text, nil
	}
	switch s["type"] {
	case "status":
		p, err := field(s, "producer_state")
		if err != nil {
			return "", err
		}
		a, err := field(s, "acquisition_state")
		if err != nil {
			return "", err
		}
		return "producer=" + p + " acquisition=" + a, nil
	case "telemetry":
		readings, ok := s["measurements"].(map[string]any)
		if !ok || len(readings) == 0 {
			return "", fmt.Errorf("summary lacks measurements")
		}
		var parts []string
		for _, n := range slices.Sorted(maps.Keys(readings)) {
			r, ok := readings[n].(map[string]any)
			if !ok {
				return "", fmt.Errorf("reading %s is malformed", n)
			}
			var vals []string
			for _, k := range []string{"value", "unit", "quality", "freshness"} {
				v, err := field(r, k)
				if err != nil {
					return "", fmt.Errorf("%s: %w", n, err)
				}
				vals = append(vals, v)
			}
			parts = append(parts, fmt.Sprintf("%s=%s %s (%s, %s)", n, vals[0], vals[1], vals[2], vals[3]))
		}
		return strings.Join(parts, " "), nil
	}
	return "", fmt.Errorf("unexpected message type %v", s["type"])
}

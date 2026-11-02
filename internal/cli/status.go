package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/openump/Hades/internal/supervisor"
)

// Status reports each supervised service of a site; it exits non-zero when
// any service is not running and ready.
func Status(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hades status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("site-dir", "", "site directory from hades init")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	site, err := LoadSite(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "hades status:", err)
		return 1
	}
	code := 0
	enc := json.NewEncoder(stdout)
	for _, name := range site.Services {
		entry := map[string]any{"service": name}
		cfg, err := supervisor.Load(filepath.Join(*dir, "services", name+".json"))
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var st supervisor.Status
			st, err = supervisor.Probe(ctx, cfg)
			cancel()
			if err == nil {
				entry["ready"], entry["child_pid"], entry["started_at"] = st.Ready, st.ChildPID, st.Started
				if !st.Ready {
					code = 1
				}
			}
		}
		if err != nil {
			entry["ready"], entry["error"] = false, err.Error()
			code = 1
		}
		enc.Encode(entry)
	}
	return code
}

package history

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
)

// Map emits only good numeric values. Quality is a separate bounded series.
func Map(cfg *sample.Config, p map[string]any) (lines []byte, keys []string, skips int, err error) {
	if err = contracts.ValidateContext(p, cfg.Metadata); err != nil {
		return
	}
	observed, _ := time.Parse(time.RFC3339Nano, p["observed_at"].(string))
	stamp := observed.UnixMilli()
	measurements := p["measurements"].(map[string]any)
	names := make([]string, 0, len(measurements))
	for name := range measurements {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		reading := measurements[name].(map[string]any)
		labels := fmt.Sprintf("asset_id=%q,measurement=%q,unit=%q,metadata_revision=%q", p["asset_id"], name, reading["unit"], p["metadata_revision"].(json.Number).String())
		quality := map[string]int{"good": 0, "uncertain": 1, "bad": 2}[reading["quality"].(string)]
		fmt.Fprintf(&out, "hades_observation_quality{%s} %d %d\n", labels, quality, stamp)
		// One collision key covers both value and quality for this configured series.
		keys = append(keys, fmt.Sprintf("%s/%d", labels, stamp))
		value, numeric := reading["value"].(json.Number)
		if numeric && reading["quality"] == "good" {
			fmt.Fprintf(&out, "hades_observation_value{%s} %s %d\n", labels, value.String(), stamp)
		} else {
			skips++
		}
	}
	lines = []byte(out.String())
	return
}

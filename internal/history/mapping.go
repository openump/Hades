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
	assetCfg, resolveErr := cfg.ResolveAsset(p["asset_id"], p["metadata_revision"])
	if resolveErr != nil {
		assetCfg = cfg
	}
	if err = contracts.ValidateContext(p, assetCfg.Metadata); err != nil {
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

// MapDerived maps a soft-sensor value to its own metric, hades_derived_value,
// never hades_observation_value, so no query over device series can include
// it. Model provenance is in the labels (bounded: one series per asset,
// measurement and model version); confidence is a sibling series. The value
// is stamped at its window end (observed_at).
func MapDerived(p map[string]any) (lines []byte, keys []string) {
	observed, _ := time.Parse(time.RFC3339Nano, p["observed_at"].(string))
	stamp := observed.UnixMilli()
	labels := fmt.Sprintf("asset_id=%q,measurement=%q,unit=%q,model_id=%q,model_version=%q",
		p["asset"], p["measurement"], p["unit"], p["model_id"], p["model_version"])
	var out strings.Builder
	fmt.Fprintf(&out, "hades_derived_value{%s} %s %d\n", labels, p["value"].(json.Number).String(), stamp)
	fmt.Fprintf(&out, "hades_derived_confidence{%s} %s %d\n", labels, p["confidence"].(json.Number).String(), stamp)
	// The collision key covers asset, measurement and model: two values from the
	// same model for the same window end conflict; another model never does.
	keys = append(keys, fmt.Sprintf("derived/%s/%s/%s@%s/%d", p["asset"], p["measurement"], p["model_id"], p["model_version"], stamp))
	return []byte(out.String()), keys
}

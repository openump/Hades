// Package normalize embeds the pinned Bento reading mapper behind strict ingress.
package normalize

import (
	"encoding/json"
	"fmt"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
	"github.com/warpstreamlabs/bento/public/bloblang"
)

type Engine struct {
	Config  *sample.Config
	mapping *bloblang.Executor
}

func New(cfg *sample.Config, mapping string) (*Engine, error) {
	executor, err := bloblang.NewEnvironment().OnlyPure().WithDisabledImports().WithMaxMapRecursion(8).Parse(mapping)
	if err != nil {
		return nil, err
	}
	return &Engine{cfg, executor}, nil
}
func (e *Engine) Map(raw []byte) ([]byte, error) {
	value, err := contracts.Decode(raw)
	if err != nil {
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("raw observation must be object")
	}
	allowed := map[string]bool{"schema_version": true, "message_id": true, "asset_id": true, "metadata_revision": true, "observed_at": true, "ingested_at": true, "source": true, "signals": true}
	if len(obj) != len(allowed) {
		return nil, fmt.Errorf("raw observation requires exactly the declared fields")
	}
	for key := range obj {
		if !allowed[key] {
			return nil, fmt.Errorf("unknown raw field %s", key)
		}
	}
	signals, ok := obj["signals"].(map[string]any)
	if !ok || len(signals) != 1 {
		return nil, fmt.Errorf("expected temperature signal only")
	}
	reading, ok := signals["temperature"].(map[string]any)
	if !ok || reading["unit"] != "deciCel" {
		return nil, fmt.Errorf("temperature must declare deciCel")
	}
	for _, key := range []string{"value", "unit", "quality"} {
		if _, exists := reading[key]; !exists {
			return nil, fmt.Errorf("missing raw reading field %s", key)
		}
	}
	// Convert only this reading to Bento's float64 representation. Envelope numbers
	// (revision and sequence in particular) stay lossless json.Number values.
	if reading["value"] != nil {
		n, ok := reading["value"].(json.Number)
		if !ok {
			return nil, fmt.Errorf("raw value must be numeric or null")
		}
		f, err := n.Float64()
		if err != nil {
			return nil, err
		}
		reading["value"] = f
	}
	mapped, err := e.mapping.Query(signals)
	if err != nil {
		return nil, err
	}
	delete(obj, "signals")
	obj["measurements"] = mapped
	normalized, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	out, err := e.Config.Validator.Validate("telemetry", normalized)
	if err != nil {
		return nil, err
	}
	if err = contracts.ValidateContext(out, e.Config.Metadata); err != nil {
		return nil, err
	}
	if err = e.Config.Registry.ValidateTopic(e.Config.Prefix+"telemetry", out["asset_id"].(string)); err != nil {
		return nil, err
	}
	return normalized, nil
}

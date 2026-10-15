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
	Config          *sample.Config
	mapping         *bloblang.Executor
	approvedNumeric bool
	opcua           bool
}

func New(cfg *sample.Config, mapping string) (*Engine, error) {
	executor, err := bloblang.NewEnvironment().OnlyPure().WithDisabledImports().WithMaxMapRecursion(8).Parse(mapping)
	if err != nil {
		return nil, err
	}
	return &Engine{Config: cfg, mapping: executor}, nil
}

// NewApprovedNumeric is an opt-in, simulator-only profile for bounded canonical
// numeric readings. Raw names/units must match the reviewed metadata exactly;
// the legacy deci-Celsius conversion profile remains unchanged.
func NewApprovedNumeric(cfg *sample.Config) (*Engine, error) {
	if cfg.Metadata["acquisition"].(map[string]any)["protocol"] != "simulator" {
		return nil, fmt.Errorf("approved numeric profile requires simulator provenance")
	}
	defs := cfg.Metadata["measurements"].(map[string]any)
	if len(defs) < 1 || len(defs) > 64 {
		return nil, fmt.Errorf("numeric profile requires 1..64 approved measurements")
	}
	for _, value := range defs {
		if value.(map[string]any)["type"] != "number" {
			return nil, fmt.Errorf("numeric profile requires numeric definitions")
		}
	}
	e, err := New(cfg, "root = this")
	if err != nil {
		return nil, err
	}
	e.approvedNumeric = true
	return e, nil
}

// NewOPCUAMock accepts one approved node per raw observation, preserving its
// original status/timestamps in envelope extensions without changing v1 schemas.
func NewOPCUAMock(cfg *sample.Config) (*Engine, error) {
	if err := sample.CheckOPCUAMock(cfg); err != nil {
		return nil, err
	}
	e, err := New(cfg, "root = this")
	if err != nil {
		return nil, err
	}
	e.approvedNumeric = true
	e.opcua = true
	return e, nil
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
	if e.opcua {
		allowed["extensions"] = true
	}
	if len(obj) != len(allowed) {
		return nil, fmt.Errorf("raw observation requires exactly the declared fields")
	}
	for key := range obj {
		if !allowed[key] {
			return nil, fmt.Errorf("unknown raw field %s", key)
		}
	}
	signals, ok := obj["signals"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected signal object")
	}
	defs := e.Config.Metadata["measurements"].(map[string]any)
	if (!e.approvedNumeric && len(signals) != 1) || (e.approvedNumeric && !e.opcua && len(signals) != len(defs)) || (e.opcua && len(signals) != 1) {
		return nil, fmt.Errorf("signal cardinality differs from approved profile")
	}
	for name, value := range signals {
		reading, ok := value.(map[string]any)
		expectedUnit := "deciCel"
		if e.approvedNumeric {
			definition, exists := defs[name].(map[string]any)
			if !exists {
				return nil, fmt.Errorf("unapproved signal %s", name)
			}
			expectedUnit = definition["unit"].(string)
		} else if name != "temperature" {
			return nil, fmt.Errorf("expected temperature signal only")
		}
		if !ok || reading["unit"] != expectedUnit {
			return nil, fmt.Errorf("signal unit differs from approved profile")
		}
		for _, key := range []string{"value", "unit", "quality"} {
			if _, exists := reading[key]; !exists {
				return nil, fmt.Errorf("missing raw reading field %s", key)
			}
		}
		// Only reading numbers enter Bento's float64 representation. Envelope revision
		// and sequence retain the original lossless json.Number representation.
		if reading["value"] != nil {
			n, ok := reading["value"].(json.Number)
			if !ok {
				return nil, fmt.Errorf("raw value must be numeric or null")
			}
			f, err := n.Float64()
			if err != nil {
				return nil, err
			}
			if !e.approvedNumeric {
				reading["value"] = f
			}
		}
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

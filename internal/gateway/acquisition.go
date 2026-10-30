package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"
)

// AcquisitionConfig is the gateway-local acquisition map. It carries settings
// the OpenUMP metadata contract deliberately does not: controller tag addresses
// and edge report-by-exception, keyed by measurement name.
//
//	{
//	  "subscribe": true,
//	  "signals": {
//	    "motor_temperature": {"address": "%DB1:0:REAL", "deadband": {"epsilon": 0.5, "max_silence_ms": 60000}}
//	  }
//	}
type AcquisitionConfig struct {
	Subscribe bool                         `json:"subscribe"`
	Signals   map[string]AcquisitionSignal `json:"signals"`
}

type AcquisitionSignal struct {
	Address  string               `json:"address,omitempty"`
	Deadband *AcquisitionDeadband `json:"deadband,omitempty"`
}

type AcquisitionDeadband struct {
	Epsilon      float64 `json:"epsilon"`
	MaxSilenceMS int64   `json:"max_silence_ms"`
}

// LoadAcquisitionConfig reads and validates an acquisition map. Unknown fields
// are rejected so a misspelt setting cannot silently disable a guardrail.
func LoadAcquisitionConfig(path string) (*AcquisitionConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("acquisition config exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cfg AcquisitionConfig
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid acquisition config: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("invalid acquisition config: trailing data")
	}
	if len(cfg.Signals) > 64 {
		return nil, fmt.Errorf("acquisition config allows at most 64 signals")
	}
	for name, signal := range cfg.Signals {
		if name == "" {
			return nil, fmt.Errorf("acquisition signal name is required")
		}
		if len(signal.Address) > 256 {
			return nil, fmt.Errorf("acquisition signal %q address is too long", name)
		}
		if band := signal.Deadband; band != nil {
			if band.Epsilon < 0 || math.IsNaN(band.Epsilon) || math.IsInf(band.Epsilon, 0) {
				return nil, fmt.Errorf("acquisition signal %q deadband epsilon must be a finite value >= 0", name)
			}
			if band.MaxSilenceMS < 1000 || band.MaxSilenceMS > 86400000 {
				return nil, fmt.Errorf("acquisition signal %q max_silence_ms must be 1000..86400000", name)
			}
		}
	}
	return &cfg, nil
}

// Deadbands returns the per-signal report-by-exception settings.
func (c *AcquisitionConfig) Deadbands() map[string]DeadbandConfig {
	out := make(map[string]DeadbandConfig)
	for name, signal := range c.Signals {
		if band := signal.Deadband; band != nil {
			out[name] = DeadbandConfig{Epsilon: band.Epsilon, MaxSilenceInterval: time.Duration(band.MaxSilenceMS) * time.Millisecond}
		}
	}
	return out
}

// TagAddresses returns the configured controller tag address per signal.
func (c *AcquisitionConfig) TagAddresses() map[string]string {
	out := make(map[string]string)
	for name, signal := range c.Signals {
		if signal.Address != "" {
			out[name] = signal.Address
		}
	}
	return out
}

// Apply copies the acquisition settings into driver and factory options,
// rejecting signals that are not measurements of the asset.
func (c *AcquisitionConfig) Apply(measurements map[string]any, options *DriverOptions, factory *FactoryOptions) error {
	for name := range c.Signals {
		if _, ok := measurements[name]; !ok {
			return fmt.Errorf("acquisition config names unknown measurement %q", name)
		}
	}
	options.Deadbands = c.Deadbands()
	options.Subscribe = c.Subscribe
	if tags := c.TagAddresses(); len(tags) > 0 {
		factory.PLC4GoTags = tags
	}
	return nil
}

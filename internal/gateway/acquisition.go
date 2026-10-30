package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"strings"
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

// labPLC4GoOptions allowlists the connection options each scheme may use on the
// lab path. Only options that address the loopback peer itself or tune timing
// are allowed; routing options that could reach past it (CIP communication
// paths, BACnet remote networks/addresses, BBMD registration, static device
// tables) are rejected.
var labPLC4GoOptions = map[string]map[string]bool{
	"s7":         {"cotp.local-rack": true, "cotp.local-slot": true, "cotp.remote-rack": true, "cotp.remote-slot": true, "controller-type": true, "pdu-size": true, "max-amq-caller": true, "max-amq-callee": true},
	"logix":      {"backplane": true, "slot": true, "force-unconnected-operation": true, "forceUnconnectedOperation": true},
	"eip":        {"backplane": true, "slot": true, "big-endian": true, "bigEndian": true, "force-unconnected-operation": true, "forceUnconnectedOperation": true},
	"ads":        {"target-ams-net-id": true, "targetAmsNetId": true, "target-ams-port": true, "targetAmsPort": true, "source-ams-net-id": true, "sourceAmsNetId": true, "source-ams-port": true, "sourceAmsPort": true},
	"bacnet-ip":  {"local-port": true, "apdutimeoutms": true, "apduretries": true},
	"modbus-tcp": {"default-unit-identifier": true, "request-timeout-ms": true, "unit": true},
}

// ValidateLabPLC4GoEndpoint enforces the lab gate for PLC4X acquisition: a
// supported scheme, a literal loopback IP host and only allowlisted options.
// Site approval is required before any other endpoint may be polled.
func ValidateLabPLC4GoEndpoint(endpoint string) error {
	connectionString, err := plc4goConnectionURL(endpoint)
	if err != nil {
		return err
	}
	ip := net.ParseIP(connectionString.Hostname())
	if ip == nil || !ip.IsLoopback() || (connectionString.Path != "" && connectionString.Path != "/") {
		return fmt.Errorf("lab plc4go endpoint requires scheme://<loopback IP>[:port][?options]; site approval is pending")
	}
	allowed := labPLC4GoOptions[connectionString.Scheme]
	for option := range connectionString.Query() {
		if !allowed[strings.ToLower(option)] && !allowed[option] {
			return fmt.Errorf("lab plc4go endpoint option %q is not allowed for %s; site approval is pending", option, connectionString.Scheme)
		}
	}
	return nil
}

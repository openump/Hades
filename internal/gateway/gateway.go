// Package gateway is a loopback-only lab wrapper around protocol drivers.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
)

// Reading is a normalized driver poll sample before envelope marshaling.
type Reading struct {
	Value      any
	Unit       string
	Quality    string
	Reason     string
	ObservedAt time.Time
	Extensions map[string]any
}

// ProtocolDriver is the common interface implemented by gateway protocol drivers.
type ProtocolDriver interface {
	Poll(ctx context.Context) (map[string]Reading, time.Time, string)
	Close() error
}

// RegisterMapping describes a single holding register in a Modbus-TCP profile.
type RegisterMapping struct {
	Name      string
	Address   uint16
	DataType  string
	ByteOrder string
	Scale     float64
	Unit      string
}

// Profile configures a Modbus-TCP register polling profile.
type Profile struct {
	MetricName string
	SlaveID    uint8
	Registers  []RegisterMapping
}

func DefaultLabProfile() Profile {
	return Profile{
		MetricName: "hades_lab",
		SlaveID:    1,
		Registers: []RegisterMapping{
			{Name: "temperature", Address: 0, DataType: "UINT16", ByteOrder: "AB", Scale: 1.0, Unit: "deciCel"},
		},
	}
}

func validateProfile(profile Profile) (Profile, error) {
	if profile.MetricName == "" {
		profile.MetricName = "hades_lab"
	}
	if profile.SlaveID == 0 {
		profile.SlaveID = 1
	}
	if len(profile.Registers) < 1 || len(profile.Registers) > 64 {
		return Profile{}, fmt.Errorf("gateway profile requires 1..64 register mappings")
	}
	seen := make(map[string]bool, len(profile.Registers))
	out := make([]RegisterMapping, len(profile.Registers))
	for i, reg := range profile.Registers {
		if reg.Name == "" || seen[reg.Name] {
			return Profile{}, fmt.Errorf("invalid or duplicate register name %q", reg.Name)
		}
		seen[reg.Name] = true
		if reg.DataType == "" {
			reg.DataType = "UINT16"
		}
		if reg.ByteOrder == "" {
			reg.ByteOrder = "AB"
		}
		if reg.Scale == 0 {
			reg.Scale = 1.0
		}
		if reg.Unit == "" {
			reg.Unit = "deciCel"
		}
		out[i] = reg
	}
	profile.Registers = out
	return profile, nil
}

func DriverConfig(endpoint string) (string, error) {
	return DriverConfigForProfile(endpoint, DefaultLabProfile())
}

func DriverConfigForProfile(endpoint string, profile Profile) (string, error) {
	return DriverConfigForProfileWithInterval(endpoint, profile, time.Second)
}

func DriverConfigForProfileWithInterval(endpoint string, profile Profile, interval time.Duration) (string, error) {
	p, err := validateProfile(profile)
	if err != nil {
		return "", err
	}
	if interval < time.Second || interval > time.Hour {
		return "", fmt.Errorf("driver interval must be 1s..1h")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || ip == nil || !ip.IsLoopback() || u.Scheme != "tcp" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("lab gateway requires literal loopback tcp://IP:port; real pilot not approved")
	}
	lines := make([]string, 0, len(p.Registers))
	for _, reg := range p.Registers {
		lines = append(lines, fmt.Sprintf("    { measurement = %q, name = %q, byte_order = %q, data_type = %q, scale = %.1f, address = [%d] }", p.MetricName, reg.Name, reg.ByteOrder, reg.DataType, reg.Scale, reg.Address))
	}
	return fmt.Sprintf(`[agent]
	interval = %q
	flush_interval = %q
	round_interval = false
  omit_hostname = true
  quiet = true
[[inputs.modbus]]
  name = %q
  controller = %q
  slave_id = %d
  timeout = "200ms"
  busy_retries = 0
  configuration_type = "register"
  holding_registers = [
%s
  ]
[[outputs.file]]
  files = ["stdout"]
  data_format = "json"
  json_timestamp_units = "1ms"
`, interval.String(), interval.String(), p.MetricName, endpoint, p.SlaveID, strings.Join(lines, ",\n")), nil
}

func DecodeReading(raw []byte) (uint16, time.Time, error) {
	readings, stamp, err := DecodeReadingsForProfile(raw, DefaultLabProfile())
	if err != nil {
		return 0, time.Time{}, err
	}
	return readings["temperature"], stamp, nil
}

func DecodeReadingsForProfile(raw []byte, profile Profile) (map[string]uint16, time.Time, error) {
	p, err := validateProfile(profile)
	if err != nil {
		return nil, time.Time{}, err
	}
	value, err := contracts.Decode(raw)
	if err != nil {
		return nil, time.Time{}, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, time.Time{}, fmt.Errorf("expected driver object")
	}
	if metrics, exists := obj["metrics"]; exists {
		items, ok := metrics.([]any)
		if !ok || len(items) != 1 || len(obj) != 1 {
			return nil, time.Time{}, fmt.Errorf("expected one driver metric")
		}
		obj, ok = items[0].(map[string]any)
		if !ok {
			return nil, time.Time{}, fmt.Errorf("invalid metric")
		}
	}
	fields, ok := obj["fields"].(map[string]any)
	if !ok || len(fields) != len(p.Registers) || obj["name"] != p.MetricName {
		return nil, time.Time{}, fmt.Errorf("unexpected driver signal/name")
	}
	out := make(map[string]uint16, len(p.Registers))
	for _, reg := range p.Registers {
		number, ok := fields[reg.Name].(json.Number)
		if !ok {
			return nil, time.Time{}, fmt.Errorf("missing numeric raw value for %s", reg.Name)
		}
		n, err := number.Int64()
		if err != nil || n < 0 || n > 65535 {
			return nil, time.Time{}, fmt.Errorf("raw value outside UINT16")
		}
		out[reg.Name] = uint16(n)
	}
	stamp, ok := obj["timestamp"].(json.Number)
	if !ok {
		return nil, time.Time{}, fmt.Errorf("missing driver timestamp")
	}
	ms, err := stamp.Int64()
	if err != nil || ms < 0 || ms > 4102444800000 {
		return nil, time.Time{}, fmt.Errorf("invalid millisecond timestamp")
	}
	return out, time.UnixMilli(ms).UTC(), nil
}

func Envelope(cfg *sample.Config, value *uint16, reason, boot string, sequence int, observed time.Time) ([]byte, error) {
	var values map[string]uint16
	if value != nil {
		values = map[string]uint16{"temperature": *value}
	}
	return EnvelopeForProfile(cfg, DefaultLabProfile(), values, reason, boot, sequence, observed)
}

func EnvelopeForProfile(cfg *sample.Config, profile Profile, values map[string]uint16, reason, boot string, sequence int, observed time.Time) ([]byte, error) {
	p, err := validateProfile(profile)
	if err != nil {
		return nil, err
	}
	signals := make(map[string]any, len(p.Registers))
	for _, reg := range p.Registers {
		var reading any
		quality := "good"
		if v, ok := values[reg.Name]; ok && values != nil {
			reading = v
		} else {
			quality = "bad"
		}
		sig := map[string]any{"value": reading, "unit": reg.Unit, "quality": quality}
		if quality == "bad" {
			sig["reason"] = reason
		}
		signals[reg.Name] = sig
	}
	env := map[string]any{
		"schema_version":    "1.0",
		"message_id":        sample.UUID(),
		"asset_id":          cfg.Metadata["asset_id"],
		"metadata_revision": cfg.Metadata["metadata_revision"],
		"observed_at":       observed.UTC().Format(sample.Timestamp),
		"ingested_at":       time.Now().UTC().Format(sample.Timestamp),
		"source": map[string]any{
			"gateway_id":           cfg.Metadata["acquisition"].(map[string]any)["gateway_id"],
			"protocol":             "modbus-tcp",
			"boot_id":              boot,
			"sequence":             sequence,
			"clock_origin":         "gateway",
			"clock_uncertainty_ms": nil,
		},
		"signals": signals,
	}
	return json.Marshal(env)
}

func EnvelopeForReading(cfg *sample.Config, name string, reading Reading, boot string, sequence int, observed time.Time) ([]byte, error) {
	defs, ok := cfg.Metadata["measurements"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata missing measurements")
	}
	definition, ok := defs[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unknown measurement %q", name)
	}
	protocol, _ := cfg.Metadata["acquisition"].(map[string]any)["protocol"].(string)
	if protocol == "" {
		return nil, fmt.Errorf("metadata missing acquisition protocol")
	}
	clockOrigin := "gateway"
	if origin, ok := reading.Extensions["opcua.timestamp_origin"].(string); ok && origin != "" {
		clockOrigin = origin
	}
	if !reading.ObservedAt.IsZero() {
		observed = reading.ObservedAt
	}
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	signal := map[string]any{
		"value":   reading.Value,
		"unit":    definition["unit"],
		"quality": reading.Quality,
	}
	if reading.Reason != "" {
		signal["reason"] = reading.Reason
	}
	env := map[string]any{
		"schema_version":    "1.0",
		"message_id":        sample.UUID(),
		"asset_id":          cfg.Metadata["asset_id"],
		"metadata_revision": cfg.Metadata["metadata_revision"],
		"observed_at":       observed.UTC().Format(sample.Timestamp),
		"ingested_at":       time.Now().UTC().Format(sample.Timestamp),
		"source": map[string]any{
			"gateway_id":           cfg.Metadata["acquisition"].(map[string]any)["gateway_id"],
			"protocol":             protocol,
			"boot_id":              boot,
			"sequence":             sequence,
			"clock_origin":         clockOrigin,
			"clock_uncertainty_ms": nil,
		},
		"signals": map[string]any{name: signal},
	}
	if len(reading.Extensions) > 0 {
		env["extensions"] = reading.Extensions
	}
	return json.Marshal(env)
}

// Package sample contains localhost-only synthetic examples, not an OT gateway.
package sample

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/openump/Hades/internal/contracts"
)

const Timestamp = "2006-01-02T15:04:05.000Z"

type Config struct {
	Validator     *contracts.Validator
	Metadata      map[string]any
	MetadataBytes []byte
	Registry      *contracts.Registry
	Prefix        string
	Interval      time.Duration
}

func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func Num(v any) float64 { n, _ := v.(json.Number).Float64(); return n }
func Load(root, path string) (*Config, error) {
	v, err := contracts.New(os.DirFS(filepath.Join(root, "schemas")))
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, contracts.MaxPayloadBytes+1))
	if err != nil {
		return nil, err
	}
	m, err := v.Validate("metadata", raw)
	if err != nil {
		return nil, err
	}
	defs := m["measurements"].(map[string]any)
	d, ok := defs["motor_temperature"].(map[string]any)
	if !ok || len(defs) != 1 || d["type"] != "number" || (d["unit"] != "Cel" && d["unit"] != "K") {
		return nil, fmt.Errorf("sample requires one numeric motor_temperature measurement in Cel or K")
	}
	if m["acquisition"].(map[string]any)["protocol"] != "simulator" {
		return nil, fmt.Errorf("sample protocol must be simulator")
	}
	if Num(m["acquisition"].(map[string]any)["expected_status_interval_ms"]) != Num(d["expected_interval_ms"]) || Num(d["expected_interval_ms"]) > 28800000 {
		return nil, fmt.Errorf("sample measurement/status intervals must agree and be at most eight hours")
	}
	id := strings.Split(m["asset_id"].(string), ":")
	h := m["hierarchy"].(map[string]any)
	asset := contracts.Asset{AssetID: m["asset_id"].(string), Enterprise: id[2], IdentitySite: id[3], AssetKey: id[4], Location: contracts.Location{Site: h["site"].(string), Area: h["area"].(string), Line: h["line"].(string), Asset: h["asset"].(string)}}
	registry, err := contracts.NewRegistry([]contracts.Asset{asset})
	if err != nil {
		return nil, err
	}
	prefix := strings.Join([]string{"openump", "v1", asset.Enterprise, asset.Location.Site, asset.Location.Area, asset.Location.Line, asset.Location.Asset, ""}, "/")
	return &Config{v, m, raw, registry, prefix, time.Duration(Num(d["expected_interval_ms"])) * time.Millisecond}, nil
}
func (c *Config) Observation(scenario string, sequence int, boot string, now time.Time) ([]byte, error) {
	if scenario != "normal" && scenario != "bad-quality" && scenario != "missing-reading" {
		return nil, fmt.Errorf("unknown scenario %q", scenario)
	}
	definition := c.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any)
	value := 67.2 + float64(sequence%10)/10
	if definition["unit"] == "K" {
		value += 273.15
	}
	value = math.Round(value*100) / 100
	reading := map[string]any{"value": value, "unit": definition["unit"], "quality": "good"}
	if scenario != "normal" {
		reading["quality"] = "bad"
		reading["reason"] = "simulated-unreliable"
	}
	if scenario == "missing-reading" {
		reading["value"] = nil
		reading["reason"] = "simulated-timeout"
	}
	acquisition := c.Metadata["acquisition"].(map[string]any)
	stamp := now.UTC().Format(Timestamp)
	p := map[string]any{"schema_version": "1.0", "message_id": UUID(), "asset_id": c.Metadata["asset_id"], "metadata_revision": c.Metadata["metadata_revision"], "observed_at": stamp, "ingested_at": stamp, "source": map[string]any{"gateway_id": acquisition["gateway_id"], "protocol": "simulator", "boot_id": boot, "sequence": sequence, "clock_origin": "gateway", "clock_uncertainty_ms": 0}, "measurements": map[string]any{"motor_temperature": reading}}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	obj, err := c.Validator.Validate("telemetry", raw)
	if err != nil {
		return nil, err
	}
	if err := contracts.ValidateContext(obj, c.Metadata); err != nil {
		return nil, err
	}
	return raw, nil
}
func (c *Config) Expiry() uint32 {
	return uint32((3*Num(c.Metadata["acquisition"].(map[string]any)["expected_status_interval_ms"]) + 999) / 1000)
}
func (c *Config) Status(kind, boot, session string, last *time.Time, degraded bool) ([]byte, error) {
	producer, acquisition := "online", "unknown"
	var stamp any
	if last != nil {
		stamp = last.UTC().Format(Timestamp)
		acquisition = "healthy"
	}
	if degraded {
		acquisition = "degraded"
	}
	if kind == "shutdown" || kind == "last_will" {
		producer = "offline"
		acquisition = "unknown"
	}
	if kind == "last_will" {
		stamp = nil
	}
	p := map[string]any{"schema_version": "1.0", "message_id": UUID(), "asset_id": c.Metadata["asset_id"], "metadata_revision": c.Metadata["metadata_revision"], "reported_at": time.Now().UTC().Format(Timestamp), "kind": kind, "source": map[string]any{"gateway_id": c.Metadata["acquisition"].(map[string]any)["gateway_id"], "boot_id": boot, "session_id": session}, "producer_state": producer, "acquisition_state": acquisition, "last_observed_at": stamp, "valid_for_ms": 3 * Num(c.Metadata["acquisition"].(map[string]any)["expected_status_interval_ms"])}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	_, err = c.Validator.Validate("status", raw)
	return raw, err
}

// Send retries copies of one immutable observation; no unbounded outgoing queue.
func Send(ctx context.Context, raw []byte, publish func(context.Context, []byte) error) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = publish(ctx, bytes.Clone(raw))
		if err == nil {
			return nil
		}
		delay := time.Duration(1<<min(attempt, 3)) * 100 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("publish attempts exhausted: %w", err)
}
func (c *Config) Inspect(topic string, raw []byte, now time.Time) (map[string]any, error) {
	if !strings.HasPrefix(topic, c.Prefix) {
		return nil, fmt.Errorf("wrong sample topic")
	}
	kind := strings.TrimPrefix(topic, c.Prefix)
	p, err := c.Validator.Validate(kind, raw)
	if err != nil {
		return nil, err
	}
	if err := c.Registry.ValidateTopic(topic, p["asset_id"].(string)); err != nil {
		return nil, err
	}
	if p["metadata_revision"] != c.Metadata["metadata_revision"] {
		return nil, fmt.Errorf("unknown sample metadata revision")
	}
	result := map[string]any{"type": kind, "asset_id": p["asset_id"], "message_id": p["message_id"], "payload": p}
	switch kind {
	case "metadata":
		if !reflect.DeepEqual(p, c.Metadata) {
			return nil, fmt.Errorf("metadata conflicts with approved sample snapshot")
		}
	case "status":
		result["producer_state"] = p["producer_state"]
		result["acquisition_state"] = p["acquisition_state"]
	case "telemetry":
		if err := contracts.ValidateContext(p, c.Metadata); err != nil {
			return nil, err
		}
		observed, _ := time.Parse(time.RFC3339Nano, p["observed_at"].(string))
		var uncertainty *float64
		if value := p["source"].(map[string]any)["clock_uncertainty_ms"]; value != nil {
			u := Num(value)
			uncertainty = &u
		}
		definition := c.Metadata["measurements"].(map[string]any)["motor_temperature"].(map[string]any)
		reading := p["measurements"].(map[string]any)["motor_temperature"].(map[string]any)
		result["observed_at"] = p["observed_at"]
		result["unit"] = reading["unit"]
		result["quality"] = reading["quality"]
		result["value"] = reading["value"]
		result["freshness"] = contracts.Freshness(observed, now, Num(definition["expected_interval_ms"]), uncertainty)
	}
	return result, nil
}

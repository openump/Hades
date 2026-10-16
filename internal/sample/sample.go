// Package sample contains localhost-only synthetic examples, not an OT gateway.
package sample

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
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

type HierarchyKey struct {
	Enterprise string
	Site       string
	Area       string
	Line       string
	Asset      string
}

type MeasurementKey struct {
	Hierarchy   HierarchyKey
	Measurement string
}

type MeasurementSpec struct {
	AssetID            string
	Hierarchy          HierarchyKey
	Name               string
	Type               string
	Unit               string
	ExpectedIntervalMS float64
}

type Catalog struct {
	Configs      []*Config
	ByAssetID    map[string]*Config
	ByHierarchy  map[HierarchyKey]*Config
	Measurements map[MeasurementKey]MeasurementSpec
	Registry     *contracts.Registry
}

var ApprovedUnits = map[string]bool{
	"1": true, "%": true, "Cel": true, "K": true,
	"bar": true, "Pa": true, "kPa": true, "m": true,
	"s": true, "Hz": true, "V": true, "A": true, "W": true,
}

var ApprovedProtocols = map[string]bool{
	"simulator":  true,
	"modbus-tcp": true,
	"opc-ua":     true,
}

type Config struct {
	TLS           *tls.Config
	Validator     *contracts.Validator
	Metadata      map[string]any
	MetadataBytes []byte
	Registry      *contracts.Registry
	Catalog       *Catalog
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
func Num(v any) float64 {
	switch n := v.(type) {
	case json.Number:
		f, _ := n.Float64()
		return f
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func HierarchyOf(m map[string]any) HierarchyKey {
	h := m["hierarchy"].(map[string]any)
	return HierarchyKey{
		Enterprise: h["enterprise"].(string),
		Site:       h["site"].(string),
		Area:       h["area"].(string),
		Line:       h["line"].(string),
		Asset:      h["asset"].(string),
	}
}

func loadSingleContext(v *contracts.Validator, path string) (*Config, contracts.Asset, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, contracts.Asset{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, contracts.MaxPayloadBytes+1))
	if err != nil {
		return nil, contracts.Asset{}, err
	}
	m, err := v.Validate("metadata", raw)
	if err != nil {
		return nil, contracts.Asset{}, err
	}
	id := strings.Split(m["asset_id"].(string), ":")
	h := m["hierarchy"].(map[string]any)
	asset := contracts.Asset{
		AssetID:      m["asset_id"].(string),
		Enterprise:   id[2],
		IdentitySite: id[3],
		AssetKey:     id[4],
		Location: contracts.Location{
			Site:  h["site"].(string),
			Area:  h["area"].(string),
			Line:  h["line"].(string),
			Asset: h["asset"].(string),
		},
	}
	prefix := strings.Join([]string{"openump", "v1", asset.Enterprise, asset.Location.Site, asset.Location.Area, asset.Location.Line, asset.Location.Asset, ""}, "/")
	cfg := &Config{
		Validator:     v,
		Metadata:      m,
		MetadataBytes: raw,
		Prefix:        prefix,
		Interval:      time.Duration(Num(m["acquisition"].(map[string]any)["expected_status_interval_ms"])) * time.Millisecond,
	}
	return cfg, asset, nil
}

// LoadCatalog loads 1..256 metadata files into a unified multi-asset, multi-measurement Catalog.
func LoadCatalog(root string, paths ...string) (*Catalog, error) {
	var clean []string
	for _, p := range paths {
		for _, part := range strings.Split(p, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				clean = append(clean, trimmed)
			}
		}
	}
	if len(clean) < 1 || len(clean) > 256 {
		return nil, fmt.Errorf("catalog requires 1..256 metadata paths")
	}
	v, err := contracts.New(os.DirFS(filepath.Join(root, "schemas")))
	if err != nil {
		return nil, err
	}
	cat := &Catalog{
		ByAssetID:    map[string]*Config{},
		ByHierarchy:  map[HierarchyKey]*Config{},
		Measurements: map[MeasurementKey]MeasurementSpec{},
	}
	var assets []contracts.Asset
	var enterprise string
	for _, p := range clean {
		cfg, asset, err := loadSingleContext(v, p)
		if err != nil {
			return nil, err
		}
		if enterprise == "" {
			enterprise = asset.Enterprise
		} else if asset.Enterprise != enterprise {
			return nil, fmt.Errorf("catalog assets must share enterprise %q", enterprise)
		}
		if _, exists := cat.ByAssetID[asset.AssetID]; exists {
			return nil, fmt.Errorf("duplicate catalog asset %s", asset.AssetID)
		}
		hKey := HierarchyOf(cfg.Metadata)
		if _, exists := cat.ByHierarchy[hKey]; exists {
			return nil, fmt.Errorf("duplicate catalog hierarchy %+v", hKey)
		}
		protocol, _ := cfg.Metadata["acquisition"].(map[string]any)["protocol"].(string)
		if !ApprovedProtocols[protocol] {
			return nil, fmt.Errorf("unapproved catalog protocol %q", protocol)
		}
		defs := cfg.Metadata["measurements"].(map[string]any)
		if len(defs) < 1 || len(defs) > 64 {
			return nil, fmt.Errorf("catalog requires 1..64 measurements per asset")
		}
		for name, rawDef := range defs {
			d := rawDef.(map[string]any)
			mType, _ := d["type"].(string)
			unit, _ := d["unit"].(string)
			if mType != "number" || !ApprovedUnits[unit] {
				return nil, fmt.Errorf("catalog measurement %s requires numeric type and approved unit", name)
			}
			mKey := MeasurementKey{Hierarchy: hKey, Measurement: name}
			cat.Measurements[mKey] = MeasurementSpec{
				AssetID:            asset.AssetID,
				Hierarchy:          hKey,
				Name:               name,
				Type:               mType,
				Unit:               unit,
				ExpectedIntervalMS: Num(d["expected_interval_ms"]),
			}
		}
		cat.Configs = append(cat.Configs, cfg)
		cat.ByAssetID[asset.AssetID] = cfg
		cat.ByHierarchy[hKey] = cfg
		assets = append(assets, asset)
	}
	registry, err := contracts.NewRegistry(assets)
	if err != nil {
		return nil, err
	}
	cat.Registry = registry
	for _, cfg := range cat.Configs {
		cfg.Registry = registry
		cfg.Catalog = cat
	}
	return cat, nil
}

func (cat *Catalog) LookupMeasurement(h HierarchyKey, measurement string) (MeasurementSpec, bool) {
	if cat == nil {
		return MeasurementSpec{}, false
	}
	if cfg, ok := cat.ByHierarchy[h]; ok {
		if defs, ok := cfg.Metadata["measurements"].(map[string]any); ok {
			if rawDef, ok := defs[measurement].(map[string]any); ok {
				return MeasurementSpec{
					AssetID:            cfg.Metadata["asset_id"].(string),
					Hierarchy:          h,
					Name:               measurement,
					Type:               rawDef["type"].(string),
					Unit:               rawDef["unit"].(string),
					ExpectedIntervalMS: Num(rawDef["expected_interval_ms"]),
				}, true
			}
		}
	}
	spec, ok := cat.Measurements[MeasurementKey{Hierarchy: h, Measurement: measurement}]
	return spec, ok
}

func LoadContext(root, path string) (*Config, error) {
	if strings.Contains(path, ",") {
		cat, err := LoadCatalog(root, path)
		if err != nil {
			return nil, err
		}
		return cat.Configs[0], nil
	}
	v, err := contracts.New(os.DirFS(filepath.Join(root, "schemas")))
	if err != nil {
		return nil, err
	}
	cfg, asset, err := loadSingleContext(v, path)
	if err != nil {
		return nil, err
	}
	registry, err := contracts.NewRegistry([]contracts.Asset{asset})
	if err != nil {
		return nil, err
	}
	cfg.Registry = registry
	hKey := HierarchyOf(cfg.Metadata)
	cat := &Catalog{
		Configs:      []*Config{cfg},
		ByAssetID:    map[string]*Config{asset.AssetID: cfg},
		ByHierarchy:  map[HierarchyKey]*Config{hKey: cfg},
		Measurements: map[MeasurementKey]MeasurementSpec{},
		Registry:     registry,
	}
	for name, rawDef := range cfg.Metadata["measurements"].(map[string]any) {
		d := rawDef.(map[string]any)
		cat.Measurements[MeasurementKey{Hierarchy: hKey, Measurement: name}] = MeasurementSpec{
			AssetID:            asset.AssetID,
			Hierarchy:          hKey,
			Name:               name,
			Type:               d["type"].(string),
			Unit:               d["unit"].(string),
			ExpectedIntervalMS: Num(d["expected_interval_ms"]),
		}
	}
	cfg.Catalog = cat
	return cfg, nil
}

// Assets returns all configured asset contexts in the catalog (or the single receiver context).
func (c *Config) Assets() []*Config {
	if c.Catalog != nil && len(c.Catalog.Configs) > 0 {
		return c.Catalog.Configs
	}
	return []*Config{c}
}

// ResolveAsset locates the matching asset context in the catalog and verifies its metadata_revision.
func (c *Config) ResolveAsset(assetID any, revision any) (*Config, error) {
	id, ok := assetID.(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("missing asset_id")
	}
	var target *Config
	if c.Metadata != nil && c.Metadata["asset_id"] == id {
		target = c
	} else if c.Catalog != nil {
		target = c.Catalog.ByAssetID[id]
	}
	if target == nil {
		return nil, fmt.Errorf("unknown catalog asset %s", id)
	}
	if revision != nil && Num(target.Metadata["metadata_revision"]) != Num(revision) {
		return nil, fmt.Errorf("exact metadata revision unavailable for %s", id)
	}
	return target, nil
}

// ResolveTopic locates the asset context in the catalog whose topic prefix matches topic.
func (c *Config) ResolveTopic(topic string) (*Config, string, error) {
	for _, candidate := range c.Assets() {
		if strings.HasPrefix(topic, candidate.Prefix) {
			kind := strings.TrimPrefix(topic, candidate.Prefix)
			return candidate, kind, nil
		}
	}
	return nil, "", fmt.Errorf("unapproved topic %s", topic)
}

func LoadReadingContext(root, path string) (*Config, error) {
	c, err := LoadContext(root, path)
	if err != nil {
		return nil, err
	}
	for _, assetCfg := range c.Assets() {
		m := assetCfg.Metadata
		defs := m["measurements"].(map[string]any)
		d, ok := defs["motor_temperature"].(map[string]any)
		if !ok || len(defs) != 1 || d["type"] != "number" || (d["unit"] != "Cel" && d["unit"] != "K") {
			return nil, fmt.Errorf("sample requires one numeric motor_temperature measurement in Cel or K")
		}
		protocol := m["acquisition"].(map[string]any)["protocol"]
		if protocol != "simulator" && protocol != "modbus-tcp" {
			return nil, fmt.Errorf("unsupported reading example protocol")
		}
		if Num(m["acquisition"].(map[string]any)["expected_status_interval_ms"]) != Num(d["expected_interval_ms"]) || Num(d["expected_interval_ms"]) > 28800000 {
			return nil, fmt.Errorf("sample measurement/status intervals must agree and be at most eight hours")
		}
	}
	return c, nil
}
func Load(root, path string) (*Config, error) {
	c, err := LoadReadingContext(root, path)
	if err != nil {
		return nil, err
	}
	if c.Metadata["acquisition"].(map[string]any)["protocol"] != "simulator" {
		return nil, fmt.Errorf("simulator requires simulator protocol")
	}
	return c, nil
}
func (c *Config) Observation(scenario string, sequence int, boot string, now time.Time) ([]byte, error) {
	if scenario != "normal" && scenario != "bad-quality" && scenario != "missing-reading" {
		return nil, fmt.Errorf("unknown scenario %q", scenario)
	}
	defs := c.Metadata["measurements"].(map[string]any)
	measurements := make(map[string]any, len(defs))
	for name, rawDef := range defs {
		definition := rawDef.(map[string]any)
		value := 67.2 + float64(sequence%10)/10
		if name == "motor_temperature" && definition["unit"] == "K" {
			value += 273.15
		} else if name != "motor_temperature" {
			if bounds, ok := definition["valid_range"].(map[string]any); ok {
				lo, hi := Num(bounds["minimum"]), Num(bounds["maximum"])
				value = lo + math.Min(1.0, (hi-lo)/2)
			} else {
				value = 1
			}
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
		measurements[name] = reading
	}
	acquisition := c.Metadata["acquisition"].(map[string]any)
	protocol, _ := acquisition["protocol"].(string)
	if protocol == "" {
		protocol = "simulator"
	}
	stamp := now.UTC().Format(Timestamp)
	p := map[string]any{"schema_version": "1.0", "message_id": UUID(), "asset_id": c.Metadata["asset_id"], "metadata_revision": c.Metadata["metadata_revision"], "observed_at": stamp, "ingested_at": stamp, "source": map[string]any{"gateway_id": acquisition["gateway_id"], "protocol": protocol, "boot_id": boot, "sequence": sequence, "clock_origin": "gateway", "clock_uncertainty_ms": 0}, "measurements": measurements}
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
	target, kind, err := c.ResolveTopic(topic)
	if err != nil {
		return nil, fmt.Errorf("wrong sample topic")
	}
	p, err := target.Validator.Validate(kind, raw)
	if err != nil {
		return nil, err
	}
	if err := c.Registry.ValidateTopic(topic, p["asset_id"].(string)); err != nil {
		return nil, err
	}
	if Num(p["metadata_revision"]) != Num(target.Metadata["metadata_revision"]) {
		return nil, fmt.Errorf("unknown sample metadata revision")
	}
	result := map[string]any{"type": kind, "asset_id": p["asset_id"], "message_id": p["message_id"], "payload": p}
	switch kind {
	case "metadata":
		if !reflect.DeepEqual(p, target.Metadata) {
			return nil, fmt.Errorf("metadata conflicts with approved sample snapshot")
		}
	case "status":
		result["producer_state"] = p["producer_state"]
		result["acquisition_state"] = p["acquisition_state"]
	case "telemetry":
		if err := contracts.ValidateContext(p, target.Metadata); err != nil {
			return nil, err
		}
		observed, _ := time.Parse(time.RFC3339Nano, p["observed_at"].(string))
		var uncertainty *float64
		if value := p["source"].(map[string]any)["clock_uncertainty_ms"]; value != nil {
			u := Num(value)
			uncertainty = &u
		}
		result["observed_at"] = p["observed_at"]
		summaries := map[string]any{}
		for name, value := range p["measurements"].(map[string]any) {
			reading := value.(map[string]any)
			definition := target.Metadata["measurements"].(map[string]any)[name].(map[string]any)
			summary := map[string]any{"unit": reading["unit"], "quality": reading["quality"], "value": reading["value"], "freshness": contracts.Freshness(observed, now, Num(definition["expected_interval_ms"]), uncertainty)}
			summaries[name] = summary
			if name == "motor_temperature" {
				for key, v := range summary {
					result[key] = v
				}
			}
		}
		result["measurements"] = summaries
	}
	return result, nil
}

// CheckOPCUAMock validates the fixed software pilot without linking a protocol
// SDK or the normalization engine into otherwise independent runtimes.
func CheckOPCUAMock(c *Config) error {
	if c.Metadata["acquisition"].(map[string]any)["protocol"] != "opc-ua" || c.Interval != time.Second {
		return fmt.Errorf("one-second OPC UA mock context required")
	}
	defs := c.Metadata["measurements"].(map[string]any)
	expected := map[string]string{"motor_temperature": "Cel", "cycle_counter": "1", "machine_state": "1"}
	if len(defs) != len(expected) {
		return fmt.Errorf("OPC UA mock requires exactly three reviewed numeric definitions")
	}
	for name, unit := range expected {
		d, ok := defs[name].(map[string]any)
		if !ok || d["type"] != "number" || d["unit"] != unit || Num(d["expected_interval_ms"]) != 1000 {
			return fmt.Errorf("OPC UA mock definition mismatch")
		}
	}
	return nil
}

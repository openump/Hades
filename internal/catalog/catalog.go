// Package catalog provides a multi-asset, multi-measurement metadata registry.
package catalog

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
)

type Catalog struct {
	inner    *sample.Catalog
	Registry *contracts.Registry
}

func Load(root string, paths []string) (*Catalog, error) {
	inner, err := sample.LoadCatalog(root, paths...)
	if err != nil {
		return nil, err
	}
	return &Catalog{inner: inner, Registry: inner.Registry}, nil
}

func (c *Catalog) Context(asset string, revision json.Number) (*sample.Config, error) {
	cfg, ok := c.inner.ByAssetID[asset]
	if !ok || sample.Num(cfg.Metadata["metadata_revision"]) != sample.Num(revision) {
		return nil, fmt.Errorf("catalog asset/revision unavailable")
	}
	return cfg, nil
}

func (c *Catalog) ContextByHierarchy(h sample.HierarchyKey) (*sample.Config, error) {
	cfg, ok := c.inner.ByHierarchy[h]
	if !ok {
		return nil, fmt.Errorf("catalog hierarchy unavailable")
	}
	return cfg, nil
}

func (c *Catalog) LookupMeasurement(h sample.HierarchyKey, measurement string) (sample.MeasurementSpec, bool) {
	return c.inner.LookupMeasurement(h, measurement)
}

func (c *Catalog) Validate(kind, topic string, raw []byte) (*sample.Config, map[string]any, error) {
	var validator *contracts.Validator
	for _, cfg := range c.inner.Configs {
		validator = cfg.Validator
		break
	}
	p, err := validator.Validate(kind, raw)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := c.Context(p["asset_id"].(string), p["metadata_revision"].(json.Number))
	if err != nil {
		return nil, nil, err
	}
	if topic != cfg.Prefix+kind {
		return nil, nil, fmt.Errorf("catalog topic mismatch")
	}
	if err = c.Registry.ValidateTopic(topic, p["asset_id"].(string)); err != nil {
		return nil, nil, err
	}
	if kind == "telemetry" {
		err = contracts.ValidateContext(p, cfg.Metadata)
	}
	if kind == "metadata" && !reflect.DeepEqual(p, cfg.Metadata) {
		err = fmt.Errorf("catalog metadata snapshot mismatch")
	}
	return cfg, p, err
}

func (c *Catalog) Assets() []string {
	ids := make([]string, 0, len(c.inner.ByAssetID))
	for id := range c.inner.ByAssetID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

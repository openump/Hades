// Package catalog loads a bounded reviewed set of immutable asset contexts.
package catalog

import (
	"encoding/json"
	"fmt"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
	"reflect"
	"sort"
	"strings"
)

type Catalog struct {
	contexts map[string]*sample.Config
	Registry *contracts.Registry
}

func Load(root string, paths []string) (*Catalog, error) {
	if len(paths) < 1 || len(paths) > 256 {
		return nil, fmt.Errorf("catalog requires 1..256 metadata paths")
	}
	c := &Catalog{contexts: map[string]*sample.Config{}}
	assets := []contracts.Asset{}
	for _, path := range paths {
		cfg, err := sample.LoadContext(root, path)
		if err != nil {
			return nil, err
		}
		id := cfg.Metadata["asset_id"].(string)
		if _, ok := c.contexts[id]; ok {
			return nil, fmt.Errorf("duplicate catalog asset")
		}
		protocol := cfg.Metadata["acquisition"].(map[string]any)["protocol"]
		if protocol != "simulator" && protocol != "modbus-tcp" && protocol != "opc-ua" {
			return nil, fmt.Errorf("unapproved catalog protocol")
		}
		for _, value := range cfg.Metadata["measurements"].(map[string]any) {
			if value.(map[string]any)["type"] != "number" {
				return nil, fmt.Errorf("numeric catalog profile required")
			}
		}
		c.contexts[id] = cfg
		// Registry revalidates location/identity uniqueness across contexts.
		identity := strings.Split(id, ":")
		h := cfg.Metadata["hierarchy"].(map[string]any)
		assets = append(assets, contracts.Asset{AssetID: id, Enterprise: identity[2], IdentitySite: identity[3], AssetKey: identity[4], Location: contracts.Location{Site: h["site"].(string), Area: h["area"].(string), Line: h["line"].(string), Asset: h["asset"].(string)}})
	}
	var err error
	c.Registry, err = contracts.NewRegistry(assets)
	return c, err
}
func (c *Catalog) Context(asset string, revision json.Number) (*sample.Config, error) {
	cfg, ok := c.contexts[asset]
	if !ok || cfg.Metadata["metadata_revision"] != revision {
		return nil, fmt.Errorf("catalog asset/revision unavailable")
	}
	return cfg, nil
}
func (c *Catalog) Validate(kind, topic string, raw []byte) (*sample.Config, map[string]any, error) {
	// All contexts share the same pinned schema validator; select only after decode.
	var validator *contracts.Validator
	for _, cfg := range c.contexts {
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
	ids := make([]string, 0, len(c.contexts))
	for id := range c.contexts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

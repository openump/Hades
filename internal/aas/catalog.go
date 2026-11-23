package aas

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/openump/Hades/internal/contracts"
)

// Metadata is an OpenUMP v1.0 metadata snapshot (the fields Hades generates).
type Metadata struct {
	SchemaVersion    string                 `json:"schema_version"`
	MessageID        string                 `json:"message_id"`
	AssetID          string                 `json:"asset_id"`
	MetadataRevision int64                  `json:"metadata_revision"`
	PublishedAt      string                 `json:"published_at"`
	DisplayName      string                 `json:"display_name"`
	AssetType        string                 `json:"asset_type"`
	Hierarchy        Hierarchy              `json:"hierarchy"`
	Acquisition      Acquisition            `json:"acquisition"`
	Measurements     map[string]Measurement `json:"measurements"`
	// Relationships and Extensions are carried through unchanged, so an
	// export validates exactly the snapshot it was given.
	Relationships []json.RawMessage `json:"relationships,omitempty"`
	Extensions    json.RawMessage   `json:"extensions,omitempty"`
}

// Hierarchy is the ISA-95 path.
type Hierarchy struct {
	Enterprise string `json:"enterprise"`
	Site       string `json:"site"`
	Area       string `json:"area"`
	Line       string `json:"line"`
	Asset      string `json:"asset"`
}

// Acquisition names the producing gateway.
type Acquisition struct {
	GatewayID                string `json:"gateway_id"`
	Protocol                 string `json:"protocol"`
	ExpectedStatusIntervalMS int64  `json:"expected_status_interval_ms"`
}

// Measurement is one declared measurement.
type Measurement struct {
	DisplayName        string      `json:"display_name"`
	Type               string      `json:"type"`
	Unit               string      `json:"unit"`
	ExpectedIntervalMS int64       `json:"expected_interval_ms"`
	ValidRange         *ValidRange `json:"valid_range,omitempty"`
}

// ValidRange bounds a numeric measurement.
type ValidRange struct {
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
}

// Mapping is an OpenUMP semantic-mapping crosswalk.
type Mapping struct {
	SchemaVersion    string                       `json:"schema_version"`
	AssetID          string                       `json:"asset_id"`
	MetadataRevision int64                        `json:"metadata_revision"`
	SemanticID       string                       `json:"semantic_id,omitempty"`
	Dictionary       string                       `json:"dictionary,omitempty"`
	AAS              *Binding                     `json:"aas,omitempty"`
	Measurements     map[string]MappedMeasurement `json:"measurements"`
}

// Binding records the AAS identifiers of the asset.
type Binding struct {
	AASID         string       `json:"aas_id,omitempty"`
	GlobalAssetID string       `json:"global_asset_id,omitempty"`
	SubmodelIDs   *SubmodelIDs `json:"submodel_ids,omitempty"`
}

// SubmodelIDs records the submodel identifiers of the asset.
type SubmodelIDs struct {
	Nameplate       string `json:"nameplate,omitempty"`
	TechnicalData   string `json:"technical_data,omitempty"`
	OperationalData string `json:"operational_data,omitempty"`
}

// MappedMeasurement is the crosswalk entry of one measurement.
type MappedMeasurement struct {
	SemanticID string `json:"semantic_id,omitempty"`
	Dictionary string `json:"dictionary,omitempty"`
	SourceTag  string `json:"source_tag,omitempty"`
	SourceUnit string `json:"source_unit,omitempty"`
}

// Catalog is one asset's reviewed metadata and semantic-mapping pair.
type Catalog struct {
	Metadata Metadata
	Mapping  Mapping
}

// Check validates both artifacts against the contracts and reconciles the
// mapping with its exact (asset_id, metadata_revision) snapshot. Generated
// artifacts pass the same gate a reviewer's `hades check` applies.
func (c Catalog) Check(v *contracts.Validator) error {
	_, err := c.check(v)
	return err
}

// check validates and reconciles the pair and returns the validated metadata.
func (c Catalog) check(v *contracts.Validator) (map[string]any, error) {
	meta, err := marshal(c.Metadata)
	if err != nil {
		return nil, err
	}
	mapping, err := marshal(c.Mapping)
	if err != nil {
		return nil, err
	}
	m, err := v.Validate("metadata", meta)
	if err != nil {
		return nil, fmt.Errorf("%s metadata: %w", c.Metadata.AssetID, err)
	}
	s, err := v.Validate("semantic-mapping", mapping)
	if err != nil {
		return nil, fmt.Errorf("%s semantic-mapping: %w", c.Metadata.AssetID, err)
	}
	if err := contracts.ReconcileSemanticMapping(s, m); err != nil {
		return nil, fmt.Errorf("%s: %w", c.Metadata.AssetID, err)
	}
	return m, nil
}

// Paths are the two files Write produces:
// <enterprise>.<site>.<asset>.metadata.json and .semantic-mapping.json.
func (c Catalog) Paths(dir string) [2]string {
	h := c.Metadata.Hierarchy
	base := filepath.Join(dir, h.Enterprise+"."+h.Site+"."+h.Asset)
	return [2]string{base + ".metadata.json", base + ".semantic-mapping.json"}
}

// WriteAll stores every pair. Unless force is set, it first refuses the whole
// set if any target exists, so a reviewed catalog is never replaced and a run
// never leaves half of a set behind; each file is then created without
// replacing one that appeared in the meantime.
func WriteAll(dir string, catalogs []Catalog, force bool) ([]string, error) {
	if !force {
		for _, c := range catalogs {
			for _, p := range c.Paths(dir) {
				if _, err := os.Lstat(p); err == nil {
					return nil, fmt.Errorf("%s exists; pass -force to replace it", p)
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			}
		}
	}
	var written []string
	for _, c := range catalogs {
		paths := c.Paths(dir)
		for i, v := range []any{c.Metadata, c.Mapping} {
			raw, err := marshal(v)
			if err != nil {
				return written, err
			}
			if err := writeFile(paths[i], raw, force); err != nil {
				return written, err
			}
			written = append(written, paths[i])
		}
	}
	return written, nil
}

// writeFile writes through a temporary file. With force it renames over the
// target; without, it links the temporary file into place, which fails if the
// target exists, so the no-clobber check is atomic.
func writeFile(path string, raw []byte, force bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hades-aas-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if force {
		return os.Rename(tmp.Name(), path)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s exists; pass -force to replace it", path)
		}
		return err
	}
	return nil
}

// marshal is the one deterministic encoding: two-space indent, map keys
// sorted (encoding/json), no HTML escaping, trailing newline.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// messageID is a deterministic UUIDv7: the time field is published_at and the
// random field is a digest of the asset and revision, so recompiling the same
// input yields byte-identical metadata.
func messageID(published time.Time, assetID string, revision int64) string {
	var u [16]byte
	sum := sha256.Sum256([]byte(assetID + "\x00" + strconv.FormatInt(revision, 10)))
	copy(u[:], sum[:])
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(published.UnixMilli()))
	copy(u[0:6], ms[2:8])
	u[6] = 0x70 | u[6]&0x0f
	u[8] = 0x80 | u[8]&0x3f
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Settings are the facts a tag table or an AAS does not carry.
type Settings struct {
	Revision                 int64
	PublishedAt              time.Time
	GatewayID, Protocol      string
	ExpectedStatusIntervalMS int64
}

func (s Settings) validate() error {
	if s.Revision < 1 {
		return fmt.Errorf("-revision must be at least 1")
	}
	if s.GatewayID == "" || s.Protocol == "" {
		return fmt.Errorf("-gateway-id and -protocol are required")
	}
	if s.ExpectedStatusIntervalMS < 1 {
		return fmt.Errorf("-status-interval-ms must be at least 1")
	}
	return nil
}

func (s Settings) metadata(h Hierarchy) Metadata {
	assetID := "urn:openump:" + h.Enterprise + ":" + h.Site + ":" + h.Asset
	at := s.PublishedAt.UTC().Truncate(time.Millisecond)
	return Metadata{
		SchemaVersion:    "1.0",
		MessageID:        messageID(at, assetID, s.Revision),
		AssetID:          assetID,
		MetadataRevision: s.Revision,
		PublishedAt:      at.Format("2006-01-02T15:04:05.000Z"),
		Hierarchy:        h,
		Acquisition:      Acquisition{GatewayID: s.GatewayID, Protocol: s.Protocol, ExpectedStatusIntervalMS: s.ExpectedStatusIntervalMS},
		Measurements:     map[string]Measurement{},
	}
}

// WriteFile writes raw atomically, refusing an existing file unless force.
func WriteFile(path string, raw []byte, force bool) error { return writeFile(path, raw, force) }

package aas

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openump/Hades/internal/contracts"
)

// Input bounds: a tag table is reviewed by a person, not streamed.
const (
	MaxInputBytes   = 8 << 20
	maxCSVRows      = 10000
	maxDiagnostics  = 50
	maxMeasurements = 64
)

var (
	segmentRe     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	measurementRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

var csvColumns = map[string]bool{
	"enterprise": true, "site": true, "area": true, "line": true, "asset": true,
	"measurement": true, "type": true, "unit": true, "expected_interval_ms": true,
	"asset_id": false, "display_name": false, "asset_display_name": false, "asset_type": false,
	"source_tag": false, "min": false, "max": false, "semantic_id": false, "dictionary": false,
}

// Diagnostics collects every problem in an input, each with its location, so
// one review pass fixes them all.
type Diagnostics []string

func (d Diagnostics) Error() string {
	more := ""
	if len(d) >= maxDiagnostics {
		more = "\n(further problems not reported)"
	}
	return strings.Join(d, "\n") + more
}

func (d *Diagnostics) add(format string, args ...any) {
	if len(*d) < maxDiagnostics {
		*d = append(*d, fmt.Sprintf(format, args...))
	}
}

// valueTypes maps tag-table data types to OpenUMP measurement types.
var valueTypes = map[string]string{
	"number": "number", "real": "number", "lreal": "number", "float": "number", "double": "number",
	"int": "number", "dint": "number", "sint": "number", "lint": "number", "uint": "number",
	"udint": "number", "usint": "number", "ulint": "number", "word": "number", "dword": "number",
	"integer": "number",
	"boolean": "boolean", "bool": "boolean",
	"string": "string", "wstring": "string",
}

type assetRows struct {
	catalog Catalog
	line    int // first row, for diagnostics about the asset
}

// ImportCSV compiles a brownfield tag table (one row per measurement, any
// number of assets) into one validated catalog per asset, sorted by asset_id.
// Nothing is returned unless every row is valid.
func ImportCSV(r io.Reader, s Settings, v *contracts.Validator) ([]Catalog, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", MaxInputBytes)
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // spreadsheet BOM
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("input is not UTF-8")
	}
	reader := csv.NewReader(bytes.NewReader(raw))
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("line 1: header: %w", err)
	}
	var diags Diagnostics
	col := map[string]int{}
	for i, name := range header {
		name = strings.ToLower(strings.TrimSpace(name))
		if _, known := csvColumns[name]; !known {
			diags.add("line 1: unknown column %q", name)
		} else if _, dup := col[name]; dup {
			diags.add("line 1: duplicate column %q", name)
		}
		col[name] = i
	}
	for name, required := range csvColumns {
		if _, ok := col[name]; required && !ok {
			diags.add("line 1: missing required column %q", name)
		}
	}
	if len(diags) > 0 {
		sort.Strings(diags)
		return nil, diags
	}
	assets := map[string]*assetRows{}
	rows := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var parse *csv.ParseError
			if errors.As(err, &parse) {
				diags.add("line %d: %v", parse.Line, parse.Err)
				break
			}
			return nil, err
		}
		line, _ := reader.FieldPos(0)
		if rows++; rows > maxCSVRows {
			diags.add("line %d: more than %d rows", line, maxCSVRows)
			break
		}
		get := func(name string) string {
			if i, ok := col[name]; ok {
				return strings.TrimSpace(record[i])
			}
			return ""
		}
		csvRow(get, line, s, assets, &diags)
		if len(diags) >= maxDiagnostics {
			break
		}
	}
	if rows == 0 {
		diags.add("line 2: no measurement rows")
	}
	ids := make([]string, 0, len(assets))
	for id := range assets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Catalog
	for _, id := range ids {
		a := assets[id]
		c := a.catalog
		if len(c.Metadata.Measurements) > maxMeasurements {
			diags.add("line %d: %s declares %d measurements; at most %d", a.line, id, len(c.Metadata.Measurements), maxMeasurements)
			continue
		}
		if len(c.Mapping.Measurements) == 0 {
			diags.add("line %d: %s has no semantic_id, source_tag or non-OpenUMP unit to record; a semantic mapping needs at least one", a.line, id)
			continue
		}
		if c.Metadata.DisplayName == "" {
			c.Metadata.DisplayName = c.Metadata.Hierarchy.Asset
		}
		if c.Metadata.AssetType == "" {
			c.Metadata.AssetType = "unclassified"
		}
		out = append(out, c)
	}
	if len(diags) > 0 {
		return nil, diags
	}
	for _, c := range out {
		if err := c.Check(v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func csvRow(get func(string) string, line int, s Settings, assets map[string]*assetRows, diags *Diagnostics) {
	before := len(*diags)
	h := Hierarchy{get("enterprise"), get("site"), get("area"), get("line"), get("asset")}
	for _, f := range []struct{ name, value string }{{"enterprise", h.Enterprise}, {"site", h.Site}, {"area", h.Area}, {"line", h.Line}, {"asset", h.Asset}} {
		if !segmentRe.MatchString(f.value) {
			diags.add("line %d: %s %q is not a lowercase segment (a-z, 0-9, inner '-', at most 64)", line, f.name, f.value)
		}
	}
	if len(*diags) > before {
		return
	}
	assetID := "urn:openump:" + h.Enterprise + ":" + h.Site + ":" + h.Asset
	if given := get("asset_id"); given != "" && given != assetID {
		diags.add("line %d: asset_id %q disagrees with the hierarchy (%s)", line, given, assetID)
		return
	}
	a, ok := assets[assetID]
	if !ok {
		m := s.metadata(h)
		a = &assetRows{line: line, catalog: Catalog{
			Metadata: m,
			Mapping:  Mapping{SchemaVersion: "1.0", AssetID: assetID, MetadataRevision: s.Revision, Measurements: map[string]MappedMeasurement{}},
		}}
		assets[assetID] = a
	}
	meta := &a.catalog.Metadata
	if meta.Hierarchy != h {
		diags.add("line %d: %s area/line %s/%s disagrees with line %d (%s/%s)", line, assetID, h.Area, h.Line, a.line, meta.Hierarchy.Area, meta.Hierarchy.Line)
	}
	for _, f := range []struct {
		name  string
		value string
		into  *string
	}{{"asset_display_name", get("asset_display_name"), &meta.DisplayName}, {"asset_type", get("asset_type"), &meta.AssetType}} {
		switch {
		case f.value == "":
		case *f.into == "":
			*f.into = f.value
		case *f.into != f.value:
			diags.add("line %d: %s %q disagrees with an earlier row of %s (%q)", line, f.name, f.value, assetID, *f.into)
		}
	}
	if t := get("asset_type"); t != "" && !segmentRe.MatchString(t) {
		diags.add("line %d: asset_type %q is not a lowercase segment", line, t)
	}
	if n := get("asset_display_name"); utf8.RuneCountInString(n) > 128 {
		diags.add("line %d: asset_display_name longer than 128 characters", line)
	}

	name := get("measurement")
	if !measurementRe.MatchString(name) {
		diags.add("line %d: measurement %q must match [a-z][a-z0-9_]{0,63}", line, name)
		return
	}
	if _, dup := meta.Measurements[name]; dup {
		diags.add("line %d: measurement %q repeated for %s", line, name, assetID)
		return
	}
	m := Measurement{DisplayName: get("display_name"), Unit: "1"}
	if m.DisplayName == "" {
		m.DisplayName = name
	} else if utf8.RuneCountInString(m.DisplayName) > 128 {
		diags.add("line %d: %s: display_name longer than 128 characters", line, name)
	}
	var known bool
	if m.Type, known = valueTypes[strings.ToLower(get("type"))]; !known {
		diags.add("line %d: %s: type %q is not number, boolean or string (or a PLC numeric type such as REAL, DINT)", line, name, get("type"))
	}
	var mapped MappedMeasurement
	sourceUnit := get("unit")
	if unit, err := NormalizeUnit(sourceUnit); err != nil {
		diags.add("line %d: %s: %v", line, name, err)
	} else {
		m.Unit = unit
		if sourceUnit != "" && sourceUnit != unit {
			mapped.SourceUnit = sourceUnit
		}
		if m.Type != "number" && unit != "1" {
			diags.add("line %d: %s: a %s measurement has unit 1, not %q", line, name, m.Type, sourceUnit)
		}
	}
	if interval, err := strconv.ParseInt(get("expected_interval_ms"), 10, 64); err != nil || interval < 1 || interval > 86400000 {
		diags.add("line %d: %s: expected_interval_ms %q must be an integer from 1 to 86400000", line, name, get("expected_interval_ms"))
	} else {
		m.ExpectedIntervalMS = interval
	}
	switch lo, hi := get("min"), get("max"); {
	case lo == "" && hi == "":
	case lo == "" || hi == "":
		diags.add("line %d: %s: give both min and max, or neither", line, name)
	case m.Type != "number":
		diags.add("line %d: %s: only a number measurement has a valid range", line, name)
	default:
		minimum, err1 := finite(lo)
		maximum, err2 := finite(hi)
		if err1 != nil || err2 != nil {
			diags.add("line %d: %s: min %q and max %q must be finite numbers", line, name, lo, hi)
		} else if minimum > maximum {
			diags.add("line %d: %s: min %s is above max %s", line, name, lo, hi)
		} else {
			m.ValidRange = &ValidRange{minimum, maximum}
		}
	}
	if id := get("semantic_id"); id != "" || get("dictionary") != "" {
		mapped.SemanticID, mapped.Dictionary = semanticPair(id, get("dictionary"), func(format string, args ...any) {
			diags.add("line %d: %s: "+format, append([]any{line, name}, args...)...)
		})
	}
	if tag := get("source_tag"); tag != "" {
		if utf8.RuneCountInString(tag) > 256 || strings.ContainsFunc(tag, isControl) {
			diags.add("line %d: %s: source_tag must be at most 256 characters without control characters", line, name)
		}
		mapped.SourceTag = tag
	}
	if utf8.RuneCountInString(mapped.SourceUnit) > 32 || strings.ContainsFunc(mapped.SourceUnit, isControl) {
		diags.add("line %d: %s: source unit must be at most 32 characters without control characters", line, name)
	}
	meta.Measurements[name] = m
	if mapped != (MappedMeasurement{}) {
		a.catalog.Mapping.Measurements[name] = mapped
	}
}

// semanticPair canonicalizes a semantic_id and resolves its dictionary: a
// given dictionary must fit the identifier's form; an absent one is inferred.
func semanticPair(id, dictionary string, report func(string, ...any)) (string, string) {
	if id == "" {
		report("dictionary %q given without semantic_id", dictionary)
		return "", ""
	}
	id = contracts.CanonicalSemanticID(id)
	if utf8.RuneCountInString(id) > 256 {
		report("semantic_id longer than 256 characters")
		return "", ""
	}
	if dictionary == "" {
		if dictionary = contracts.InferDictionary(id); dictionary == "" {
			report("semantic_id %q is not an ECLASS IRDI, an IEC CDD IRDI or a URI", id)
			return "", ""
		}
		return id, dictionary
	}
	switch dictionary {
	case "eclass", "iec_cdd", "opcua_companion", "custom_uri":
	default:
		report("dictionary %q is not eclass, iec_cdd, opcua_companion or custom_uri", dictionary)
		return "", ""
	}
	if !contracts.MatchesDictionary(dictionary, id) {
		report("semantic_id %q does not have the form %s uses", id, dictionary)
		return "", ""
	}
	return id, dictionary
}

func finite(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err == nil && (math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) > 1e308) {
		err = fmt.Errorf("not finite")
	}
	return f, err
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

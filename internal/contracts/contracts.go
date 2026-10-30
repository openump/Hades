// Package contracts validates the OpenUMP 1.0 profile without network access.
package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const MaxPayloadBytes = 65536

type Error struct{ Code, Detail string }

func (e *Error) Error() string                    { return e.Code + ": " + e.Detail }
func fail(code, format string, args ...any) error { return &Error{code, fmt.Sprintf(format, args...)} }

type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("unregistered schema resource %q; network loading disabled", url)
}

// Validator compiles trusted local schemas once and can be reused concurrently.
type Validator struct{ schemas map[string]*jsonschema.Schema }

func New(files fs.FS) (*Validator, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseLoader(offlineLoader{})
	// Current schemas use an absolute-end lookahead. RE2's $ already means absolute end.
	// Other unsupported ECMAScript constructs fail compilation instead of being approximated.
	c.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
		if strings.HasSuffix(pattern, `$(?![\s\S])`) {
			pattern = strings.TrimSuffix(pattern, `(?![\s\S])`)
		}
		return regexp.Compile(pattern)
	})
	paths, err := fs.Glob(files, "v1/*.schema.json")
	if err != nil {
		return nil, err
	}
	if len(paths) < 4 {
		return nil, fmt.Errorf("expected at least the four telemetry v1 schema resources, found %d", len(paths))
	}
	seen := map[string]bool{}
	for _, path := range paths {
		raw, err := fs.ReadFile(files, path)
		if err != nil {
			return nil, err
		}
		doc, err := Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		obj, ok := doc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: schema must be an object", path)
		}
		id, ok := obj["$id"].(string)
		if !ok || seen[id] {
			return nil, fmt.Errorf("%s: missing or duplicate schema ID", path)
		}
		seen[id] = true
		if obj["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			return nil, fmt.Errorf("%s: unsupported schema dialect", path)
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, err
		}
	}
	v := &Validator{schemas: map[string]*jsonschema.Schema{}}
	kinds, err := profileKinds(seen)
	if err != nil {
		return nil, err
	}
	for _, kind := range kinds {
		schema, err := c.Compile("urn:openump:schema:1.0:" + kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", kind, err)
		}
		v.schemas[kind] = schema
	}
	return v, nil
}

// profileKinds selects the message kinds to compile from the schema IDs present.
// The telemetry profile is required and the control profile is all-or-nothing;
// each additive Edge AI schema is compiled when present. Unknown IDs fail.
func profileKinds(ids map[string]bool) ([]string, error) {
	const prefix = "urn:openump:schema:1.0:"
	profiles := [][]string{{"common", "telemetry", "metadata", "status"}, {"command", "command-receipt", "actuation-policy"}}
	optional := []string{"model-manifest", "inference-event"}
	known := map[string]bool{}
	var kinds []string
	for i, profile := range profiles {
		present := 0
		for _, kind := range profile {
			known[prefix+kind] = true
			if ids[prefix+kind] {
				present++
			}
		}
		switch {
		case present == len(profile):
			kinds = append(kinds, profile...)
		case i == 0 || present > 0:
			return nil, fmt.Errorf("incomplete %s schema profile", []string{"telemetry", "control"}[i])
		}
	}
	for _, kind := range optional {
		known[prefix+kind] = true
		if ids[prefix+kind] {
			kinds = append(kinds, kind)
		}
	}
	for id := range ids {
		if !known[id] {
			return nil, fmt.Errorf("unknown schema resource %q", id)
		}
	}
	return kinds, nil
}

// Decode rejects oversized, non-UTF-8, duplicate-key, non-finite, and deep JSON.
func Decode(raw []byte) (any, error) {
	if len(raw) > MaxPayloadBytes || !utf8.Valid(raw) {
		return nil, fail("wire", "payload exceeds byte limit or contains invalid UTF-8")
	}
	if err := checkSurrogates(raw); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := readValue(d, 0)
	if err != nil {
		return nil, fail("wire", "%v", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fail("wire", "unexpected trailing JSON")
	}
	return value, nil
}
func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("JSON nesting exceeds 64")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("invalid object key")
				}
				if _, exists := object[name]; exists {
					return nil, fmt.Errorf("duplicate object key %q", name)
				}
				child, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				object[name] = child
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("unterminated object")
			}
			return object, nil
		case '[':
			array := []any{}
			for d.More() {
				child, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("unterminated array")
			}
			return array, nil
		}
		return nil, fmt.Errorf("unexpected delimiter")
	case json.Number:
		n, err := value.Float64()
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("non-finite number")
		}
		if n == 0 {
			mantissa := strings.Split(strings.ToLower(string(value)), "e")[0]
			if strings.Trim(mantissa, "+-.0") != "" {
				return nil, fmt.Errorf("number underflows binary64")
			}
			return json.Number("0"), nil // Avoid huge exponents reaching arbitrary-precision schema arithmetic.
		}
		return value, nil
	default:
		return value, nil
	}
}
func checkSurrogates(raw []byte) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return fail("wire", "truncated Unicode escape")
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return fail("wire", "invalid Unicode escape")
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return fail("wire", "unpaired Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return fail("wire", "unpaired Unicode surrogate")
			}
			i += 6
		} else if n >= 0xdc00 && n <= 0xdfff {
			return fail("wire", "unpaired Unicode surrogate")
		}
	}
	return nil
}
func (v *Validator) Validate(kind string, raw []byte) (map[string]any, error) {
	if kind == "common" || v.schemas[kind] == nil {
		return nil, fail("schema", "unsupported message type %q", kind)
	}
	value, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	if err := v.schemas[kind].Validate(value); err != nil {
		return nil, fail("schema", "%v", err)
	}
	object := value.(map[string]any)
	if kind == "metadata" {
		if err := metadataSemantics(object); err != nil {
			return nil, err
		}
	}
	if kind == "actuation-policy" {
		if err := policySemantics(object); err != nil {
			return nil, err
		}
	}
	if kind == "command-receipt" {
		if err := receiptSemantics(object); err != nil {
			return nil, err
		}
	}
	if kind == "model-manifest" {
		if err := manifestSemantics(object); err != nil {
			return nil, err
		}
	}
	if kind == "inference-event" {
		if err := inferenceEventSemantics(object); err != nil {
			return nil, err
		}
	}
	return object, nil
}

var segment = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

type Location struct{ Site, Area, Line, Asset string }
type Asset struct {
	AssetID      string `json:"asset_id"`
	Enterprise   string
	IdentitySite string `json:"identity_site"`
	AssetKey     string `json:"asset_key"`
	DisplayName  string `json:"display_name"`
	Location     Location
}
type Registry struct{ paths map[string]string }

func NewRegistry(assets []Asset) (*Registry, error) {
	r := &Registry{paths: map[string]string{}}
	used := map[string]bool{}
	if len(assets) == 0 {
		return nil, fail("namespace", "empty asset registry")
	}
	for _, asset := range assets {
		values := []string{asset.Enterprise, asset.IdentitySite, asset.AssetKey, asset.Location.Site, asset.Location.Area, asset.Location.Line, asset.Location.Asset}
		for _, value := range values {
			if !segment.MatchString(value) {
				return nil, fail("namespace", "invalid registry identifier %q", value)
			}
		}
		if asset.AssetID != "urn:openump:"+strings.Join(values[:3], ":") {
			return nil, fail("namespace", "identity disagrees with allocation fields")
		}
		path := strings.Join([]string{asset.Enterprise, asset.Location.Site, asset.Location.Area, asset.Location.Line, asset.Location.Asset}, "/")
		if _, exists := r.paths[asset.AssetID]; exists || used[path] {
			return nil, fail("namespace", "duplicate asset identity or publication path")
		}
		used[path] = true
		r.paths[asset.AssetID] = path
	}
	return r, nil
}
func (r *Registry) ValidateTopic(topic, assetID string) error {
	p := strings.Split(topic, "/")
	if len(topic) > 512 || len(p) != 8 || p[0] != "openump" || p[1] != "v1" {
		return fail("namespace", "invalid publication topic shape")
	}
	for _, value := range p[2:7] {
		if !segment.MatchString(value) {
			return fail("namespace", "invalid hierarchy segment %q", value)
		}
	}
	if p[7] != "telemetry" && p[7] != "metadata" && p[7] != "status" {
		return fail("namespace", "unsupported topic message type")
	}
	path, ok := r.paths[assetID]
	if !ok || path != strings.Join(p[2:7], "/") {
		return fail("namespace", "unknown asset or mismatched registry location")
	}
	return nil
}

func number(value any) float64 { n, _ := value.(json.Number).Float64(); return n }
func metadataSemantics(m map[string]any) error {
	enterprise := strings.Split(m["asset_id"].(string), ":")[2]
	if m["hierarchy"].(map[string]any)["enterprise"] != enterprise {
		return fail("context", "asset ownership disagrees with hierarchy")
	}
	for _, value := range m["measurements"].(map[string]any) {
		d := value.(map[string]any)
		if bounds, ok := d["valid_range"].(map[string]any); ok && number(bounds["minimum"]) > number(bounds["maximum"]) {
			return fail("context", "inverted measurement range")
		}
	}
	seen := map[string]bool{}
	if relations, ok := m["relationships"].([]any); ok {
		for _, value := range relations {
			r := value.(map[string]any)
			target := r["target_asset_id"].(string)
			key := r["relation"].(string) + ":" + target
			if target == m["asset_id"] || seen[key] || strings.Split(target, ":")[2] != enterprise {
				return fail("context", "self, duplicate, or cross-enterprise relationship")
			}
			seen[key] = true
			if end, ok := r["effective_until"].(string); ok {
				start, _ := time.Parse(time.RFC3339Nano, r["effective_from"].(string))
				until, _ := time.Parse(time.RFC3339Nano, end)
				if !until.After(start) {
					return fail("context", "nonpositive relationship effective period")
				}
			}
		}
	}
	return nil
}

// ValidateContext compares already schema-validated telemetry and metadata.
func ValidateContext(t, m map[string]any) error {
	if t["asset_id"] != m["asset_id"] || number(t["metadata_revision"]) != number(m["metadata_revision"]) {
		return fail("context", "exact metadata revision unavailable")
	}
	source := t["source"].(map[string]any)
	acquisition := m["acquisition"].(map[string]any)
	if source["gateway_id"] != acquisition["gateway_id"] || source["protocol"] != acquisition["protocol"] {
		return fail("context", "acquisition source mismatch")
	}
	definitions := m["measurements"].(map[string]any)
	for name, value := range t["measurements"].(map[string]any) {
		reading := value.(map[string]any)
		definition, ok := definitions[name].(map[string]any)
		if !ok || reading["unit"] != definition["unit"] {
			return fail("context", "%s: missing definition or wrong unit", name)
		}
		value := reading["value"]
		if value == nil {
			continue
		}
		valid := false
		switch definition["type"] {
		case "number":
			_, valid = value.(json.Number)
		case "boolean":
			_, valid = value.(bool)
		case "string":
			_, valid = value.(string)
		}
		if !valid {
			return fail("context", "%s: incorrect value type", name)
		}
		if bounds, ok := definition["valid_range"].(map[string]any); ok && reading["quality"] == "good" {
			n := number(value)
			if n < number(bounds["minimum"]) || n > number(bounds["maximum"]) {
				return fail("context", "%s: good value outside range", name)
			}
		}
	}
	return nil
}

// Freshness uses trusted timestamps and validated metadata; uncertainty is combined clock uncertainty.
func Freshness(observed, now time.Time, intervalMS float64, uncertaintyMS *float64) string {
	if uncertaintyMS == nil {
		return "unknown"
	}
	age := float64(now.Sub(observed)) / float64(time.Millisecond)
	if age < -*uncertaintyMS {
		return "unknown"
	}
	if age > 3*intervalMS+*uncertaintyMS {
		return "stale"
	}
	return "fresh"
}

package contracts

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Dictionary identifier forms (Hades-045, ADR-0005). The schema accepts any of
// them; semanticMappingSemantics requires the form to match the dictionary.
var (
	eclassIRDI = regexp.MustCompile(`^(irdi:)?[0-9]{4}-1#[0-9]{2}-[A-Z0-9]{6}#[0-9]{3}$`)
	iecIRDI    = regexp.MustCompile(`^0112/2///[0-9]{5}(_[0-9]+)?#[A-Z]{3}[0-9]{3}#[0-9]{3}$`)
	uriForm    = regexp.MustCompile(`^(urn:[a-z0-9][a-z0-9-]{0,31}:|https?://)[!-~]+$`)
)

// semanticIDMatches reports whether an identifier has the form its dictionary
// uses: ECLASS needs an ECLASS IRDI; IEC CDD an IEC IRDI or a URI; OPC UA
// companion specifications and custom dictionaries a URI.
func semanticIDMatches(dictionary, id string) bool {
	switch dictionary {
	case "eclass":
		return eclassIRDI.MatchString(id)
	case "iec_cdd":
		return iecIRDI.MatchString(id) || uriForm.MatchString(id)
	case "opcua_companion", "custom_uri":
		return uriForm.MatchString(id)
	}
	return false
}

// CanonicalSemanticID drops the optional "irdi:" prefix of an ECLASS IRDI, so
// one concept has one spelling in generated artifacts.
func CanonicalSemanticID(id string) string {
	if eclassIRDI.MatchString(id) {
		return strings.TrimPrefix(id, "irdi:")
	}
	return id
}

// semanticMappingSemantics checks rules a schema cannot express: every
// semantic_id has the form of its declared dictionary.
func semanticMappingSemantics(m map[string]any) error {
	if id, ok := m["semantic_id"].(string); ok && !semanticIDMatches(m["dictionary"].(string), id) {
		return fail("context", "asset semantic_id does not match dictionary %s", m["dictionary"])
	}
	names := make([]string, 0)
	for name := range m["measurements"].(map[string]any) {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d := m["measurements"].(map[string]any)[name].(map[string]any)
		if id, ok := d["semantic_id"].(string); ok && !semanticIDMatches(d["dictionary"].(string), id) {
			return fail("context", "%s: semantic_id does not match dictionary %s", name, d["dictionary"])
		}
	}
	return nil
}

// ReconcileSemanticMapping binds a schema-valid semantic mapping to its paired
// metadata snapshot: the same asset, the exact metadata_revision, and only
// measurements that snapshot declares. A mapping is meaningless, and is
// refused, against any other revision.
func ReconcileSemanticMapping(mapping, metadata map[string]any) error {
	if mapping["asset_id"] != metadata["asset_id"] {
		return fail("context", "semantic mapping is for another asset")
	}
	a, okA := revisionOf(mapping["metadata_revision"])
	b, okB := revisionOf(metadata["metadata_revision"])
	if !okA || !okB || a != b {
		return fail("context", "semantic mapping is pinned to another metadata_revision")
	}
	declared, _ := metadata["measurements"].(map[string]any)
	names := make([]string, 0)
	for name := range mapping["measurements"].(map[string]any) {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := declared[name]; !ok {
			return fail("context", "%s is not declared in metadata revision %v", name, metadata["metadata_revision"])
		}
	}
	return nil
}

// revisionOf reads a revision decoded either by Validate (json.Number) or by
// plain json.Unmarshal (float64), so the exported reconciliation never panics.
func revisionOf(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		return int64(n), n == float64(int64(n))
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// InferDictionary names the dictionary an identifier's form implies: an ECLASS
// IRDI is "eclass", an IEC IRDI "iec_cdd", any other accepted URI
// "custom_uri". It returns "" for an identifier of no accepted form.
func InferDictionary(id string) string {
	switch {
	case eclassIRDI.MatchString(id):
		return "eclass"
	case iecIRDI.MatchString(id):
		return "iec_cdd"
	case uriForm.MatchString(id):
		return "custom_uri"
	}
	return ""
}

// MatchesDictionary reports whether id has the identifier form dictionary
// uses, the rule semantic-mapping validation applies.
func MatchesDictionary(dictionary, id string) bool { return semanticIDMatches(dictionary, id) }

// IsBidiControl reports the Unicode bidirectional formatting characters:
// marks, embeddings, overrides and isolates. Text carrying them can display
// reordered, so Hades views neutralize them and AAS export refuses them.
func IsBidiControl(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

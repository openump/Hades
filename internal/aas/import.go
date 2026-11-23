package aas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openump/Hades/internal/contracts"
)

// AASOptions supply what an AAS does not carry: the ISA-95 placement and the
// acquisition facts. Asset overrides the segment derived from the shell's
// idShort and needs an environment with one shell. ExpectedIntervalMS is used
// for elements without an openump:expected_interval_ms qualifier. Submodels,
// when set, restricts measurements to the submodels with those idShorts.
type AASOptions struct {
	Settings
	Enterprise, Site, Area, Line, Asset string
	AssetType                           string
	ExpectedIntervalMS                  int64
	Submodels                           []string
}

const maxCollectionDepth = 8

// ImportAAS compiles an AAS V3.0 JSON environment into one validated catalog
// per shell, sorted by asset_id. Property and Range elements become
// measurements; units come from IEC 61360 concept descriptions. Warnings
// report facts that were read but could not be recorded.
func ImportAAS(r io.Reader, o AASOptions, v *contracts.Validator) ([]Catalog, []string, error) {
	if err := o.Settings.validate(); err != nil {
		return nil, nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > MaxInputBytes || !utf8.Valid(raw) {
		return nil, nil, fmt.Errorf("input exceeds %d bytes or is not UTF-8", MaxInputBytes)
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	var env Environment
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(&env); err != nil {
		return nil, nil, fmt.Errorf("AAS environment: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, nil, fmt.Errorf("AAS environment: trailing data")
	}
	if len(env.AssetAdministrationShells) == 0 {
		return nil, nil, fmt.Errorf("AAS environment has no assetAdministrationShells")
	}
	if o.Asset != "" && len(env.AssetAdministrationShells) > 1 {
		return nil, nil, fmt.Errorf("-asset names one asset; the environment has %d shells", len(env.AssetAdministrationShells))
	}
	var diags Diagnostics
	for _, f := range []struct{ name, value string }{{"enterprise", o.Enterprise}, {"site", o.Site}, {"area", o.Area}, {"line", o.Line}} {
		if !segmentRe.MatchString(f.value) {
			diags.add("-%s %q is not a lowercase segment", f.name, f.value)
		}
	}
	if o.AssetType != "" && !segmentRe.MatchString(o.AssetType) {
		diags.add("-asset-type %q is not a lowercase segment", o.AssetType)
	}
	if len(diags) > 0 {
		return nil, nil, diags
	}
	submodels := map[string]Submodel{}
	for _, sm := range env.Submodels {
		submodels[sm.ID] = sm
	}
	concepts := map[string]ConceptDescription{}
	for _, cd := range env.ConceptDescriptions {
		concepts[cd.ID] = cd
	}
	only := map[string]bool{}
	for _, s := range o.Submodels {
		only[s] = true
	}
	imp := importer{o: o, submodels: submodels, concepts: concepts, only: only, diags: &diags}
	var out []Catalog
	seen := map[string]string{}
	for i, shell := range env.AssetAdministrationShells {
		where := fmt.Sprintf("shell %d (%s)", i, shell.IDShort)
		c, ok := imp.shell(shell, where)
		if !ok {
			continue
		}
		if prev, dup := seen[c.Metadata.AssetID]; dup {
			diags.add("%s: asset %s already produced by %s", where, c.Metadata.AssetID, prev)
			continue
		}
		seen[c.Metadata.AssetID] = where
		out = append(out, c)
	}
	if len(diags) > 0 {
		return nil, nil, diags
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.AssetID < out[j].Metadata.AssetID })
	for _, c := range out {
		if err := c.Check(v); err != nil {
			return nil, nil, err
		}
	}
	return out, imp.warnings, nil
}

type importer struct {
	o         AASOptions
	submodels map[string]Submodel
	concepts  map[string]ConceptDescription
	only      map[string]bool
	diags     *Diagnostics
	warnings  []string
}

func (imp *importer) warn(format string, args ...any) {
	imp.warnings = append(imp.warnings, fmt.Sprintf(format, args...))
}

func (imp *importer) shell(shell Shell, where string) (Catalog, bool) {
	before := len(*imp.diags)
	asset := imp.o.Asset
	if asset == "" {
		asset = assetFromIDShort(shell.IDShort)
	}
	if !segmentRe.MatchString(asset) {
		imp.diags.add("%s: asset segment %q is not a lowercase segment; pass -asset", where, asset)
		return Catalog{}, false
	}
	h := Hierarchy{imp.o.Enterprise, imp.o.Site, imp.o.Area, imp.o.Line, asset}
	m := imp.o.Settings.metadata(h)
	for _, x := range shell.Extensions {
		if x.Name == ExtensionAssetID && x.Value != m.AssetID {
			imp.diags.add("%s: the shell records asset %s, not %s; check the hierarchy flags", where, x.Value, m.AssetID)
		}
	}
	m.DisplayName = text(shell.DisplayName)
	if m.DisplayName == "" {
		m.DisplayName = or(shell.IDShort, asset)
	}
	if utf8.RuneCountInString(m.DisplayName) > 128 {
		imp.diags.add("%s: display name longer than 128 characters", where)
	}
	m.AssetType = imp.o.AssetType
	if t := strings.TrimPrefix(shell.AssetInformation.AssetType, assetTypePrefix); m.AssetType == "" && segmentRe.MatchString(t) {
		m.AssetType = t
	}
	if m.AssetType == "" {
		m.AssetType = "unclassified"
	}
	s := Mapping{SchemaVersion: "1.0", AssetID: m.AssetID, MetadataRevision: m.MetadataRevision, Measurements: map[string]MappedMeasurement{}}
	binding := Binding{}
	if identifier(shell.ID) {
		binding.AASID = shell.ID
	} else {
		imp.warn("%s: shell id %q is not a URN or URL; not recorded in the mapping", where, shell.ID)
	}
	if g := shell.AssetInformation.GlobalAssetID; identifier(g) {
		binding.GlobalAssetID = g
	} else if g != "" {
		imp.warn("%s: globalAssetId %q is not a URN or URL; not recorded in the mapping", where, g)
	}
	ids := SubmodelIDs{}
	for _, ref := range shell.Submodels {
		id := ref.key()
		sm, ok := imp.submodels[id]
		if !ok {
			imp.diags.add("%s: submodel %q is referenced but not in the environment", where, id)
			continue
		}
		semantic := strings.ToLower(sm.SemanticID.key())
		switch {
		case strings.HasPrefix(semantic, "https://admin-shell.io/zvei/nameplate/"):
			if identifier(sm.ID) {
				ids.Nameplate = sm.ID
			}
			continue // static nameplate data, not measurements
		case semantic == strings.ToLower(OperationalDataSemanticID):
			if identifier(sm.ID) {
				ids.OperationalData = sm.ID
			}
			continue // latest values of measurements defined elsewhere
		case strings.HasPrefix(semantic, "https://admin-shell.io/zvei/technicaldata/"):
			if identifier(sm.ID) {
				ids.TechnicalData = sm.ID
			}
			if len(imp.only) == 0 || imp.only[sm.IDShort] {
				imp.technicalData(sm, &m, &s, where)
			}
			continue
		}
		if len(imp.only) == 0 || imp.only[sm.IDShort] {
			imp.elements(sm.SubmodelElements, 0, &m, &s, where+" "+sm.IDShort)
		}
	}
	if ids != (SubmodelIDs{}) {
		binding.SubmodelIDs = &ids
	}
	if binding != (Binding{}) {
		s.AAS = &binding
	}
	if len(m.Measurements) == 0 {
		imp.diags.add("%s: no Property or Range elements to import", where)
	} else if len(m.Measurements) > maxMeasurements {
		imp.diags.add("%s: %d measurements; at most %d", where, len(m.Measurements), maxMeasurements)
	} else if len(s.Measurements) == 0 {
		imp.diags.add("%s: no measurement carries a semantic id, source tag or source unit; a semantic mapping needs at least one", where)
	}
	return Catalog{m, s}, len(*imp.diags) == before
}

// technicalData reads the IDTA 02003 layout: measurements from
// TechnicalProperties, the asset's class from ProductClassifications.
func (imp *importer) technicalData(sm Submodel, m *Metadata, s *Mapping, where string) {
	for _, e := range sm.SubmodelElements {
		switch e.IDShort {
		case "TechnicalProperties":
			children, err := imp.children(e)
			if err != nil {
				imp.diags.add("%s TechnicalProperties: %v", where, err)
				continue
			}
			imp.elements(children, 1, m, s, where+" TechnicalProperties")
		case "ProductClassifications":
			classes, _ := imp.children(e)
			for _, class := range classes {
				props, _ := imp.children(class)
				var id, system string
				for _, p := range props {
					var value string
					if json.Unmarshal(p.Value, &value) != nil {
						continue
					}
					switch p.IDShort {
					case "ProductClassId":
						id = contracts.CanonicalSemanticID(strings.TrimSpace(value))
					case "ProductClassificationSystem":
						system = strings.TrimSpace(value)
					}
				}
				if id == "" || s.SemanticID != "" {
					continue
				}
				dict := contracts.InferDictionary(id)
				for name, label := range classificationSystem {
					if strings.EqualFold(label, system) && contracts.MatchesDictionary(name, id) {
						dict = name
					}
				}
				if dict != "" && utf8.RuneCountInString(id) <= 256 {
					s.SemanticID, s.Dictionary = id, dict
				} else {
					imp.warn("%s: product class %q is not an IRDI or URI; not recorded", where, id)
				}
			}
		}
	}
}

func (imp *importer) children(e Element) ([]Element, error) {
	if e.ModelType != "SubmodelElementCollection" {
		return nil, fmt.Errorf("%s is a %s, not a SubmodelElementCollection", e.IDShort, e.ModelType)
	}
	var children []Element
	if len(e.Value) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(e.Value, &children); err != nil {
		return nil, fmt.Errorf("%s: %w", e.IDShort, err)
	}
	return children, nil
}

func (imp *importer) elements(elements []Element, depth int, m *Metadata, s *Mapping, where string) {
	skipped := 0
	for _, e := range elements {
		switch e.ModelType {
		case "Property", "Range":
			imp.measurement(e, m, s, where)
		case "SubmodelElementCollection":
			if depth >= maxCollectionDepth {
				imp.diags.add("%s %s: collections nested deeper than %d", where, e.IDShort, maxCollectionDepth)
				continue
			}
			children, err := imp.children(e)
			if err != nil {
				imp.diags.add("%s: %v", where, err)
				continue
			}
			imp.elements(children, depth+1, m, s, where+" "+e.IDShort)
		default:
			skipped++
		}
	}
	if skipped > 0 {
		imp.warn("%s: skipped %d element(s) that are not Property, Range or a collection", where, skipped)
	}
}

func (imp *importer) measurement(e Element, m *Metadata, s *Mapping, where string) {
	name := snake(e.IDShort)
	at := where + " " + e.IDShort
	if !measurementRe.MatchString(name) {
		imp.diags.add("%s: idShort does not give a measurement name ([a-z][a-z0-9_]{0,63})", at)
		return
	}
	if _, dup := m.Measurements[name]; dup {
		imp.diags.add("%s: measurement %q defined twice", at, name)
		return
	}
	d := Measurement{}
	switch {
	case e.ValueType == "xs:boolean":
		d.Type = "boolean"
	case e.ValueType == "xs:string":
		d.Type = "string"
	case numericXSD[e.ValueType]:
		d.Type = "number"
	default:
		imp.diags.add("%s: valueType %q has no OpenUMP measurement type", at, e.ValueType)
		return
	}
	cd, hasCD := imp.concept(e)
	d.DisplayName = text(e.DisplayName)
	if d.DisplayName == "" && hasCD {
		d.DisplayName = text(cd.PreferredName)
	}
	if d.DisplayName == "" {
		d.DisplayName = e.IDShort
	}
	if utf8.RuneCountInString(d.DisplayName) > 128 {
		imp.diags.add("%s: display name longer than 128 characters", at)
	}
	var mapped MappedMeasurement
	d.Unit = "1"
	if source := cd.Unit; hasCD && source != "" {
		unit, err := NormalizeUnit(source)
		if err != nil {
			imp.diags.add("%s: %v", at, err)
		} else {
			d.Unit = unit
			if source != unit {
				mapped.SourceUnit = source
			}
		}
	} else if d.Type == "number" {
		imp.diags.add("%s: no IEC 61360 unit for a numeric element (unit \"1\" must be stated, not assumed)", at)
	}
	if d.Type != "number" && d.Unit != "1" {
		imp.diags.add("%s: a %s element has unit 1, not %q", at, d.Type, cd.Unit)
	}
	if v, ok := e.qualifier(QualifierSourceUnit); ok {
		mapped.SourceUnit = v
	}
	if v, ok := e.qualifier(QualifierSourceTag); ok {
		mapped.SourceTag = v
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{{"source tag", mapped.SourceTag, 256}, {"source unit", mapped.SourceUnit, 32}} {
		if utf8.RuneCountInString(f.value) > f.max || strings.ContainsFunc(f.value, isControl) {
			imp.diags.add("%s: %s must be at most %d characters without control characters", at, f.name, f.max)
		}
	}
	d.ExpectedIntervalMS = imp.o.ExpectedIntervalMS
	if v, ok := e.qualifier(QualifierInterval); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n < 1 || n > 86400000 {
			imp.diags.add("%s: qualifier %s %q is not an integer from 1 to 86400000", at, QualifierInterval, v)
			return
		}
		d.ExpectedIntervalMS = n
	}
	if d.ExpectedIntervalMS < 1 || d.ExpectedIntervalMS > 86400000 {
		imp.diags.add("%s: no expected interval from 1 to 86400000 ms (qualifier %s or -expected-interval-ms)", at, QualifierInterval)
	}
	if e.ModelType == "Range" {
		switch {
		case d.Type != "number":
			imp.diags.add("%s: a Range must be numeric", at)
		case e.Min == nil || e.Max == nil || *e.Min == "" || *e.Max == "":
			imp.warn("%s: open range not recorded (OpenUMP needs both bounds)", at)
		default:
			lo, err1 := finite(*e.Min)
			hi, err2 := finite(*e.Max)
			if err1 != nil || err2 != nil || lo > hi {
				imp.diags.add("%s: range %q..%q is not a finite, ordered pair", at, *e.Min, *e.Max)
			} else {
				d.ValidRange = &ValidRange{lo, hi}
			}
		}
	}
	if ref := e.SemanticID; ref != nil && ref.Type == "ExternalReference" {
		id := contracts.CanonicalSemanticID(strings.TrimSpace(ref.key()))
		if dict := contracts.InferDictionary(id); dict != "" && utf8.RuneCountInString(id) <= 256 {
			mapped.SemanticID, mapped.Dictionary = id, dict
			if given, ok := e.qualifier(QualifierDictionary); ok {
				if !contracts.MatchesDictionary(given, id) {
					imp.diags.add("%s: qualifier %s %q does not fit semanticId %q", at, QualifierDictionary, given, id)
				}
				mapped.Dictionary = given
			}
		} else {
			imp.warn("%s: semanticId %q is not an IRDI or URI; not recorded", at, id)
		}
	}
	m.Measurements[name] = d
	if mapped != (MappedMeasurement{}) {
		s.Measurements[name] = mapped
	}
}

// concept finds the IEC 61360 content for an element through its semanticId
// or a supplemental semantic id.
func (imp *importer) concept(e Element) (IEC61360, bool) {
	refs := append([]Reference{}, e.SupplementalSemanticIDs...)
	if e.SemanticID != nil {
		refs = append([]Reference{*e.SemanticID}, refs...)
	}
	for _, ref := range refs {
		cd, ok := imp.concepts[ref.key()]
		if !ok { // an "irdi:"-prefixed reference names the same ECLASS concept
			cd, ok = imp.concepts[contracts.CanonicalSemanticID(ref.key())]
		}
		if !ok {
			continue
		}
		for _, eds := range cd.EmbeddedDataSpecifications {
			if eds.DataSpecificationContent.ModelType == "DataSpecificationIec61360" {
				return eds.DataSpecificationContent, true
			}
		}
	}
	return IEC61360{}, false
}

var numericXSD = map[string]bool{
	"xs:double": true, "xs:float": true, "xs:decimal": true, "xs:integer": true,
	"xs:long": true, "xs:int": true, "xs:short": true, "xs:byte": true,
	"xs:unsignedLong": true, "xs:unsignedInt": true, "xs:unsignedShort": true, "xs:unsignedByte": true,
	"xs:nonNegativeInteger": true, "xs:positiveInteger": true, "xs:nonPositiveInteger": true, "xs:negativeInteger": true,
}

// snake turns an idShort (MotorTemperature, motor_temperature, RPM) into an
// OpenUMP measurement name (motor_temperature, rpm).
func snake(id string) string {
	runes := []rune(id)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// text picks the English text of a language-string list, else the first.
func text(list []LangString) string {
	for _, l := range list {
		if strings.EqualFold(l.Language, "en") || strings.HasPrefix(strings.ToLower(l.Language), "en-") {
			return strings.TrimSpace(l.Text)
		}
	}
	if len(list) > 0 {
		return strings.TrimSpace(list[0].Text)
	}
	return ""
}

func identifier(id string) bool {
	return len(id) <= 2000 && contracts.InferDictionary(id) == "custom_uri"
}

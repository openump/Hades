package aas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openump/Hades/internal/contracts"
)

// Snapshots are the optional live inputs of an export, each already
// schema-validated by the caller (nil when absent).
type Snapshots struct {
	Status    map[string]any
	Telemetry map[string]any
}

// Export projects a verified catalog into an AAS V3.0 environment: one shell,
// a Nameplate and a TechnicalData submodel referencing their IDTA templates,
// an OperationalData submodel when snapshots are given, and one IEC 61360
// concept description per measurement. The output is deterministic: the same
// inputs give byte-identical JSON.
//
// It is metamodel-valid and template-referenced, not a complete template
// instance: only elements Hades knows are emitted (docs/operations/aas.md).
func Export(c Catalog, snap Snapshots, v *contracts.Validator) (Environment, error) {
	metadata, err := c.check(v)
	if err != nil {
		return Environment{}, err
	}
	m, s := c.Metadata, c.Mapping
	if err := pinned(m, metadata, snap); err != nil {
		return Environment{}, err
	}
	h := m.Hierarchy
	ids := defaultIDs(h)
	if s.AAS != nil {
		ids.AASID = or(s.AAS.AASID, ids.AASID)
		ids.GlobalAssetID = or(s.AAS.GlobalAssetID, ids.GlobalAssetID)
		if sm := s.AAS.SubmodelIDs; sm != nil {
			ids.SubmodelIDs.Nameplate = or(sm.Nameplate, ids.SubmodelIDs.Nameplate)
			ids.SubmodelIDs.TechnicalData = or(sm.TechnicalData, ids.SubmodelIDs.TechnicalData)
			ids.SubmodelIDs.OperationalData = or(sm.OperationalData, ids.SubmodelIDs.OperationalData)
		}
	}
	names := make([]string, 0, len(m.Measurements))
	for name := range m.Measurements {
		names = append(names, name)
	}
	sort.Strings(names)

	concept := func(name string) string {
		return conceptPrefix + h.Enterprise + ":" + h.Site + ":" + h.Asset + ":" + name
	}
	semantics := func(e *Element, name string) {
		if id := s.Measurements[name].SemanticID; id != "" {
			e.SemanticID = external(id)
			e.SupplementalSemanticIDs = []Reference{modelRef("ConceptDescription", concept(name))}
		} else {
			ref := modelRef("ConceptDescription", concept(name))
			e.SemanticID = &ref
		}
	}

	var technical []Element
	var concepts []ConceptDescription
	for _, name := range names {
		d := m.Measurements[name]
		mapped := s.Measurements[name]
		e := Element{ModelType: "Property", IDShort: name, DisplayName: en(d.DisplayName), ValueType: xsdType[d.Type]}
		if d.ValidRange != nil {
			e.ModelType = "Range"
			lo, hi := xsdDouble(d.ValidRange.Minimum), xsdDouble(d.ValidRange.Maximum)
			e.Min, e.Max = &lo, &hi
		}
		semantics(&e, name)
		e.Qualifiers = append(e.Qualifiers, Qualifier{Type: QualifierInterval, ValueType: "xs:long", Value: strconv.FormatInt(d.ExpectedIntervalMS, 10)})
		if mapped.SourceTag != "" {
			e.Qualifiers = append(e.Qualifiers, Qualifier{Type: QualifierSourceTag, ValueType: "xs:string", Value: mapped.SourceTag})
		}
		if mapped.SourceUnit != "" {
			e.Qualifiers = append(e.Qualifiers, Qualifier{Type: QualifierSourceUnit, ValueType: "xs:string", Value: mapped.SourceUnit})
		}
		if mapped.SemanticID != "" && mapped.Dictionary != contracts.InferDictionary(mapped.SemanticID) {
			// opcua_companion, or iec_cdd for a URI: not recoverable from the form.
			e.Qualifiers = append(e.Qualifiers, Qualifier{Type: QualifierDictionary, ValueType: "xs:string", Value: mapped.Dictionary})
		}
		technical = append(technical, e)

		cd := ConceptDescription{ModelType: "ConceptDescription", ID: concept(name), IDShort: name,
			EmbeddedDataSpecifications: []EmbeddedDataSpecification{{
				DataSpecification: *external(IEC61360SpecID),
				DataSpecificationContent: IEC61360{ModelType: "DataSpecificationIec61360",
					PreferredName: en(d.DisplayName), Unit: d.Unit, DataType: iecType[d.Type]},
			}}}
		if mapped.SemanticID != "" {
			cd.IsCaseOf = []Reference{*external(mapped.SemanticID)}
		}
		concepts = append(concepts, cd)
	}

	var techElements []Element
	if s.SemanticID != "" {
		techElements = append(techElements, collection("ProductClassifications", []Element{
			collection("ProductClassification01", []Element{
				property("ProductClassificationSystem", "xs:string", classificationSystem[s.Dictionary]),
				property("ProductClassId", "xs:string", s.SemanticID),
			}),
		}))
	}
	techElements = append(techElements, collection("TechnicalProperties", technical))

	submodels := []Submodel{
		{ModelType: "Submodel", ID: ids.SubmodelIDs.Nameplate, IDShort: "Nameplate", Kind: "Instance",
			SemanticID: external(NameplateSemanticID),
			SubmodelElements: []Element{{ModelType: "MultiLanguageProperty", IDShort: "ManufacturerProductDesignation",
				Value: mustJSON(en(m.DisplayName))}}},
		{ModelType: "Submodel", ID: ids.SubmodelIDs.TechnicalData, IDShort: "TechnicalData", Kind: "Instance",
			SemanticID: external(TechnicalDataSemanticID), SubmodelElements: techElements},
	}
	if operational := operationalElements(m, snap, names, semantics); len(operational) > 0 {
		submodels = append(submodels, Submodel{ModelType: "Submodel", ID: ids.SubmodelIDs.OperationalData,
			IDShort: "OperationalData", Kind: "Instance", SemanticID: external(OperationalDataSemanticID),
			Description:      en("Latest OpenUMP values with quality; not an IDTA 02008 Time Series instance."),
			SubmodelElements: operational})
	}
	refs := make([]Reference, 0, len(submodels))
	for _, sm := range submodels {
		refs = append(refs, modelRef("Submodel", sm.ID))
	}
	shell := Shell{ModelType: "AssetAdministrationShell", ID: ids.AASID, IDShort: shellIDShort(h.Asset),
		DisplayName: en(m.DisplayName),
		Extensions: []Extension{
			{Name: ExtensionAssetID, ValueType: "xs:string", Value: m.AssetID},
			{Name: ExtensionRevision, ValueType: "xs:long", Value: strconv.FormatInt(m.MetadataRevision, 10)},
		},
		AssetInformation: AssetInformation{AssetKind: "Instance", GlobalAssetID: ids.GlobalAssetID, AssetType: assetTypePrefix + m.AssetType},
		Submodels:        refs}
	return Environment{AssetAdministrationShells: []Shell{shell}, Submodels: submodels, ConceptDescriptions: concepts}, nil
}

// operationalElements renders the status and the latest telemetry values.
func operationalElements(m Metadata, snap Snapshots, names []string, semantics func(*Element, string)) []Element {
	var out []Element
	if st := snap.Status; st != nil {
		out = append(out,
			property("ProducerState", "xs:string", st["producer_state"].(string)),
			property("AcquisitionState", "xs:string", st["acquisition_state"].(string)),
			property("StatusReportedAt", "xs:dateTime", st["reported_at"].(string)))
	}
	if t := snap.Telemetry; t != nil {
		readings := t["measurements"].(map[string]any)
		for _, name := range names {
			r, ok := readings[name].(map[string]any)
			if !ok {
				continue
			}
			e := Element{ModelType: "Property", IDShort: name, DisplayName: en(m.Measurements[name].DisplayName),
				ValueType: xsdType[m.Measurements[name].Type]}
			switch value := r["value"].(type) {
			case json.Number:
				e.Value = jsonString(string(value))
			case bool:
				e.Value = jsonString(strconv.FormatBool(value))
			case string:
				e.Value = jsonString(value)
			}
			semantics(&e, name)
			e.Qualifiers = []Qualifier{
				{Type: QualifierQuality, ValueType: "xs:string", Value: r["quality"].(string)},
				{Type: QualifierObserved, ValueType: "xs:dateTime", Value: t["observed_at"].(string)},
			}
			out = append(out, e)
		}
	}
	return out
}

// pinned refuses snapshots of another asset or metadata revision: an export
// shows live values only under the exact catalog that defines them.
func pinned(m Metadata, metadata map[string]any, snap Snapshots) error {
	if t := snap.Telemetry; t != nil {
		if err := contracts.ValidateContext(t, metadata); err != nil {
			return fmt.Errorf("telemetry: %w", err)
		}
	}
	if st := snap.Status; st != nil {
		if st["asset_id"] != m.AssetID || fmt.Sprint(st["metadata_revision"]) != strconv.FormatInt(m.MetadataRevision, 10) {
			return fmt.Errorf("status: snapshot is not for %s revision %d", m.AssetID, m.MetadataRevision)
		}
	}
	return nil
}

const (
	conceptPrefix   = "urn:openump:concept:"
	assetTypePrefix = "urn:openump:asset-type:"
)

var (
	xsdType = map[string]string{"number": "xs:double", "boolean": "xs:boolean", "string": "xs:string"}
	iecType = map[string]string{"number": "REAL_MEASURE", "boolean": "BOOLEAN", "string": "STRING"}
	// classificationSystem names the dictionary in TechnicalData's
	// ProductClassificationSystem property.
	classificationSystem = map[string]string{"eclass": "ECLASS", "iec_cdd": "IEC CDD", "opcua_companion": "OPC UA Companion Specification", "custom_uri": "URI"}
)

func defaultIDs(h Hierarchy) Binding {
	base := "urn:openump:aas:" + h.Enterprise + ":" + h.Site + ":" + h.Asset
	return Binding{AASID: base, GlobalAssetID: "urn:openump:" + h.Enterprise + ":" + h.Site + ":" + h.Asset,
		SubmodelIDs: &SubmodelIDs{Nameplate: base + ":nameplate", TechnicalData: base + ":technical-data", OperationalData: base + ":operational-data"}}
}

// shellIDShort turns an asset segment into an AAS idShort
// ([a-zA-Z][a-zA-Z0-9_]*); assetFromIDShort reverses it.
func shellIDShort(asset string) string {
	id := strings.ReplaceAll(asset, "-", "_")
	if id[0] < 'a' || id[0] > 'z' {
		id = idShortPrefix + id
	}
	return id
}

const idShortPrefix = "asset_"

func assetFromIDShort(id string) string {
	if rest, ok := strings.CutPrefix(id, idShortPrefix); ok && rest != "" && rest[0] >= '0' && rest[0] <= '9' {
		id = rest
	}
	return strings.ToLower(strings.ReplaceAll(id, "_", "-"))
}

func xsdDouble(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func en(text string) []LangString { return []LangString{{Language: "en", Text: text}} }

func property(idShort, valueType, value string) Element {
	return Element{ModelType: "Property", IDShort: idShort, ValueType: valueType, Value: jsonString(value)}
}

func collection(idShort string, elements []Element) Element {
	return Element{ModelType: "SubmodelElementCollection", IDShort: idShort, Value: mustJSON(elements)}
}

// mustJSON encodes a nested value without HTML escaping, as marshal does.
func mustJSON(v any) json.RawMessage {
	raw, err := marshal(v)
	if err != nil {
		panic(err) // only Hades-built values reach here
	}
	return json.RawMessage(bytes.TrimSpace(raw))
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Marshal is the deterministic encoding of an export.
func Marshal(env Environment) ([]byte, error) { return marshal(env) }

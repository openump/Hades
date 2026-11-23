// Package aas compiles between OpenUMP v1.0 catalogs and the IDTA Asset
// Administration Shell (AAS V3.0) JSON serialization (Hades-045, ADR-0005).
//
// Import turns a brownfield tag CSV or an AAS JSON environment into a reviewed
// OpenUMP metadata + semantic-mapping pair; Export projects a verified pair,
// with optional status and telemetry snapshots, into an AAS environment. Both
// directions are boundary compilers: the edge wire contract stays OpenUMP v1.0.
//
// Only the slice of the AAS metamodel Hades reads or writes is modelled here.
// Fields Hades does not know are ignored on import and never invented on export.
package aas

import "encoding/json"

// Environment is the AAS V3.0 JSON serialization root.
type Environment struct {
	AssetAdministrationShells []Shell              `json:"assetAdministrationShells,omitempty"`
	Submodels                 []Submodel           `json:"submodels,omitempty"`
	ConceptDescriptions       []ConceptDescription `json:"conceptDescriptions,omitempty"`
}

// Shell is an AssetAdministrationShell.
type Shell struct {
	ModelType        string           `json:"modelType"`
	ID               string           `json:"id"`
	IDShort          string           `json:"idShort,omitempty"`
	DisplayName      []LangString     `json:"displayName,omitempty"`
	Extensions       []Extension      `json:"extensions,omitempty"`
	AssetInformation AssetInformation `json:"assetInformation"`
	Submodels        []Reference      `json:"submodels,omitempty"`
}

// AssetInformation identifies the asset a shell describes.
type AssetInformation struct {
	AssetKind     string `json:"assetKind"`
	GlobalAssetID string `json:"globalAssetId,omitempty"`
	AssetType     string `json:"assetType,omitempty"`
}

// Submodel is an AAS submodel instance.
type Submodel struct {
	ModelType        string       `json:"modelType"`
	ID               string       `json:"id"`
	IDShort          string       `json:"idShort,omitempty"`
	Kind             string       `json:"kind,omitempty"`
	SemanticID       *Reference   `json:"semanticId,omitempty"`
	Description      []LangString `json:"description,omitempty"`
	SubmodelElements []Element    `json:"submodelElements,omitempty"`
}

// Element is one submodel element. The metamodel's element types share this
// struct; ModelType selects which fields apply. Value is a JSON string for a
// Property, a language-string array for a MultiLanguageProperty and an element
// array for a SubmodelElementCollection.
type Element struct {
	ModelType   string       `json:"modelType"`
	IDShort     string       `json:"idShort,omitempty"`
	DisplayName []LangString `json:"displayName,omitempty"`
	SemanticID  *Reference   `json:"semanticId,omitempty"`
	// SupplementalSemanticIDs links Hades' per-measurement concept
	// description when semanticId names a shared dictionary concept.
	SupplementalSemanticIDs []Reference     `json:"supplementalSemanticIds,omitempty"`
	Qualifiers              []Qualifier     `json:"qualifiers,omitempty"`
	ValueType               string          `json:"valueType,omitempty"`
	Value                   json.RawMessage `json:"value,omitempty"`
	Min                     *string         `json:"min,omitempty"`
	Max                     *string         `json:"max,omitempty"`
}

// Reference is an ExternalReference or a ModelReference.
type Reference struct {
	Type string `json:"type"`
	Keys []Key  `json:"keys"`
}

// Key is one reference key.
type Key struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Qualifier carries Hades facts the metamodel has no field for.
type Qualifier struct {
	Kind      string `json:"kind,omitempty"`
	Type      string `json:"type"`
	ValueType string `json:"valueType"`
	Value     string `json:"value,omitempty"`
}

// Extension carries OpenUMP lineage on the shell.
type Extension struct {
	Name      string `json:"name"`
	ValueType string `json:"valueType,omitempty"`
	Value     string `json:"value,omitempty"`
}

// LangString is a language-tagged text.
type LangString struct {
	Language string `json:"language"`
	Text     string `json:"text"`
}

// ConceptDescription defines a concept, here with IEC 61360 content.
type ConceptDescription struct {
	ModelType                  string                      `json:"modelType"`
	ID                         string                      `json:"id"`
	IDShort                    string                      `json:"idShort,omitempty"`
	IsCaseOf                   []Reference                 `json:"isCaseOf,omitempty"`
	EmbeddedDataSpecifications []EmbeddedDataSpecification `json:"embeddedDataSpecifications,omitempty"`
}

// EmbeddedDataSpecification binds a data specification to its content.
type EmbeddedDataSpecification struct {
	DataSpecification        Reference `json:"dataSpecification"`
	DataSpecificationContent IEC61360  `json:"dataSpecificationContent"`
}

// IEC61360 is DataSpecificationIec61360 content.
type IEC61360 struct {
	ModelType     string       `json:"modelType"`
	PreferredName []LangString `json:"preferredName,omitempty"`
	Unit          string       `json:"unit,omitempty"`
	DataType      string       `json:"dataType,omitempty"`
}

// Template and specification identifiers Hades references. They are cited,
// not claimed: see docs/operations/aas.md for which template fields are
// emitted.
const (
	NameplateSemanticID     = "https://admin-shell.io/zvei/nameplate/2/0/Nameplate"
	TechnicalDataSemanticID = "https://admin-shell.io/ZVEI/TechnicalData/Submodel/1/2"
	// OperationalDataSemanticID is a Hades identifier: the latest values are
	// not an IDTA 02008 Time Series instance.
	OperationalDataSemanticID = "urn:openump:aas:submodel:operational-data:1.0"
	IEC61360SpecID            = "https://admin-shell.io/DataSpecificationTemplates/DataSpecificationIec61360/3/0"

	// Qualifier and extension types Hades writes and reads back.
	QualifierInterval   = "openump:expected_interval_ms"
	QualifierSourceTag  = "openump:source_tag"
	QualifierSourceUnit = "openump:source_unit"
	QualifierDictionary = "openump:dictionary"
	QualifierQuality    = "openump:quality"
	QualifierObserved   = "openump:observed_at"
	ExtensionAssetID    = "openump:asset_id"
	ExtensionRevision   = "openump:metadata_revision"
)

func external(value string) *Reference {
	return &Reference{Type: "ExternalReference", Keys: []Key{{Type: "GlobalReference", Value: value}}}
}

func modelRef(keyType, value string) Reference {
	return Reference{Type: "ModelReference", Keys: []Key{{Type: keyType, Value: value}}}
}

// key is the identifier a semanticId resolves to (its last key, which
// for the one-key references Hades reads is the only key).
func (r *Reference) key() string {
	if r == nil || len(r.Keys) == 0 {
		return ""
	}
	return r.Keys[len(r.Keys)-1].Value
}

func (e Element) qualifier(kind string) (string, bool) {
	for _, q := range e.Qualifiers {
		if q.Type == kind {
			return q.Value, true
		}
	}
	return "", false
}

func jsonString(s string) json.RawMessage { return mustJSON(s) }

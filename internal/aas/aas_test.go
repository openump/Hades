package aas

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/contracts"
)

const root = "../.."

func validator(t *testing.T) *contracts.Validator {
	t.Helper()
	v, err := contracts.New(os.DirFS(root + "/schemas"))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func load(t *testing.T, v *contracts.Validator, kind, path string, into any) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(root + "/" + path)
	if err != nil {
		t.Fatal(err)
	}
	object, err := v.Validate(kind, raw)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatal(err)
		}
	}
	return object
}

func example(t *testing.T, v *contracts.Validator) (Catalog, Snapshots) {
	var c Catalog
	load(t, v, "metadata", "examples/site/metadata.json", &c.Metadata)
	load(t, v, "semantic-mapping", "examples/contracts/semantic-mapping/semantic-mapping.json", &c.Mapping)
	return c, Snapshots{
		Status:    load(t, v, "status", "examples/aas/status.json", nil),
		Telemetry: load(t, v, "telemetry", "examples/aas/telemetry.json", nil),
	}
}

var pressSettings = Settings{Revision: 1, PublishedAt: time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC),
	GatewayID: "simulator-01", Protocol: "simulator", ExpectedStatusIntervalMS: 1000}

func exportBytes(t *testing.T, c Catalog, snap Snapshots, v *contracts.Validator) []byte {
	t.Helper()
	env, err := Export(c, snap, v)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The committed golden export is what the example catalog compiles to, byte
// for byte; examples/aas/press-04.aas.json is validated against the official
// AAS V3.0.6 schema by tests/test_aas_export.py.
func TestGoldenExportIsDeterministic(t *testing.T) {
	v := validator(t)
	c, snap := example(t, v)
	first := exportBytes(t, c, snap, v)
	for i := 0; i < 3; i++ {
		if again := exportBytes(t, c, snap, v); !bytes.Equal(first, again) {
			t.Fatal("export is not deterministic")
		}
	}
	golden, err := os.ReadFile(root + "/examples/aas/press-04.aas.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, golden) {
		t.Fatal("export differs from examples/aas/press-04.aas.json; regenerate it with hades aas export and review the diff")
	}
}

func importAAS(t *testing.T, raw []byte, o AASOptions, v *contracts.Validator) ([]Catalog, []string) {
	t.Helper()
	cs, warnings, err := ImportAAS(bytes.NewReader(raw), o, v)
	if err != nil {
		t.Fatal(err)
	}
	return cs, warnings
}

var pressPlacement = AASOptions{Settings: pressSettings, Enterprise: "acme", Site: "berlin", Area: "assembly", Line: "line-01"}

// Export then import preserves the catalog: asset_id, semantic ids, types,
// units, ranges, intervals, source lineage and AAS identifiers.
func TestRoundTripPreservesCatalog(t *testing.T) {
	v := validator(t)
	c, snap := example(t, v)
	for _, s := range []Snapshots{{}, snap} {
		cs, warnings := importAAS(t, exportBytes(t, c, s, v), pressPlacement, v)
		if len(cs) != 1 || len(warnings) != 0 {
			t.Fatalf("catalogs %d, warnings %v", len(cs), warnings)
		}
		got := cs[0]
		want := c
		want.Metadata.MessageID = got.Metadata.MessageID // generated, not carried by the AAS
		want.Metadata.Relationships = nil                // the example's is empty; the AAS carries none
		if s.Status == nil {
			// Without snapshots there is no OperationalData submodel to name.
			binding, ids := *want.Mapping.AAS, *want.Mapping.AAS.SubmodelIDs
			ids.OperationalData = ""
			binding.SubmodelIDs = &ids
			want.Mapping.AAS = &binding
		}
		if !reflect.DeepEqual(got.Metadata, want.Metadata) {
			t.Fatalf("metadata changed:\n got %+v\nwant %+v", got.Metadata, want.Metadata)
		}
		if !reflect.DeepEqual(got.Mapping, want.Mapping) {
			t.Fatalf("mapping changed:\n got %+v\nwant %+v", got.Mapping, want.Mapping)
		}
	}
}

func TestCSVImport(t *testing.T) {
	v := validator(t)
	raw, err := os.ReadFile(root + "/examples/aas/tags.csv")
	if err != nil {
		t.Fatal(err)
	}
	s := Settings{Revision: 3, PublishedAt: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC), GatewayID: "plc-01", Protocol: "s7", ExpectedStatusIntervalMS: 10000}
	cs, err := ImportCSV(bytes.NewReader(raw), s, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Metadata.AssetID != "urn:openump:acme:berlin:press-04" || cs[1].Metadata.AssetID != "urn:openump:acme:berlin:pump-02" {
		t.Fatalf("assets %+v", cs)
	}
	press := cs[0]
	if m := press.Metadata.Measurements["motor_temperature"]; m.Unit != "Cel" || m.Type != "number" || m.ValidRange == nil || m.ValidRange.Minimum != -40 || m.ValidRange.Maximum != 150 {
		t.Fatalf("motor_temperature %+v", m)
	}
	if got := press.Mapping.Measurements["motor_temperature"]; got != (MappedMeasurement{SemanticID: "0173-1#02-AAB991#008", Dictionary: "eclass", SourceTag: "DB102.DBD12", SourceUnit: "°C"}) {
		t.Fatalf("mapping %+v", got)
	}
	if m := press.Metadata.Measurements["door_closed"]; m.Type != "boolean" || m.Unit != "1" || m.ExpectedIntervalMS != 500 {
		t.Fatalf("door_closed %+v", m)
	}
	if press.Metadata.DisplayName != "Hydraulic Press" || press.Metadata.AssetType != "hydraulic-press" {
		t.Fatalf("asset %+v", press.Metadata)
	}
	if got := cs[1].Mapping.Measurements["motor_current"]; got.Dictionary != "custom_uri" || got.SourceTag != "ns=2;s=Pump02.Current" {
		t.Fatalf("pump mapping %+v", got)
	}
	// Deterministic, and each pair exports and imports back unchanged.
	again, err := ImportCSV(bytes.NewReader(raw), s, v)
	if err != nil || !reflect.DeepEqual(again, cs) {
		t.Fatal("CSV import is not deterministic")
	}
	for _, c := range cs {
		h := c.Metadata.Hierarchy
		o := AASOptions{Settings: s, Enterprise: h.Enterprise, Site: h.Site, Area: h.Area, Line: h.Line}
		back, _ := importAAS(t, exportBytes(t, c, Snapshots{}, v), o, v)
		c.Metadata.MessageID = back[0].Metadata.MessageID
		// CSV import records no AAS binding; export assigns the default one.
		back[0].Mapping.AAS = nil
		if !reflect.DeepEqual(back[0], c) {
			t.Fatalf("%s: round trip changed the catalog:\n got %+v\nwant %+v", c.Metadata.AssetID, back[0], c)
		}
	}
}

func TestCSVDiagnostics(t *testing.T) {
	v := validator(t)
	header := "enterprise,site,area,line,asset,measurement,type,unit,expected_interval_ms,min,max,semantic_id,dictionary,source_tag,asset_id\n"
	good := "acme,berlin,a,l,press-04,temp,REAL,°C,1000,0,100,,,DB1.DBD0,\n"
	for _, tc := range []struct{ name, csv, want string }{
		{"unknown column", "enterprise,site,area,line,asset,measurement,type,unit,expected_interval_ms,colour\n", `line 1: unknown column "colour"`},
		{"missing column", "enterprise,site,area,line,asset,measurement,type,unit\n", `line 1: missing required column "expected_interval_ms"`},
		{"no rows", header, "no measurement rows"},
		{"unmappable unit", header + "acme,berlin,a,l,press-04,temp,REAL,degF,1000,,,,,t,\n", `line 2: temp: unit "degF" has no OpenUMP v1.0 equivalent`},
		{"range on boolean", header + "acme,berlin,a,l,press-04,on,BOOL,,1000,0,1,,,t,\n", "line 2: on: only a number measurement has a valid range"},
		{"half range", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,0,,,,t,\n", "line 2: temp: give both min and max"},
		{"inverted range", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,9,1,,,t,\n", "line 2: temp: min 9 is above max 1"},
		{"non-finite range", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,0,Inf,,,t,\n", "must be finite numbers"},
		{"unknown type", header + "acme,berlin,a,l,press-04,temp,TIME,s,1000,,,,,t,\n", `line 2: temp: type "TIME"`},
		{"unit on boolean", header + "acme,berlin,a,l,press-04,on,BOOL,V,1000,,,,,t,\n", "line 2: on: a boolean measurement has unit 1"},
		{"duplicate measurement", header + good + good, `line 3: measurement "temp" repeated`},
		{"uppercase segment", header + "acme,Berlin,a,l,press-04,temp,REAL,Cel,1000,,,,,t,\n", `line 2: site "Berlin" is not a lowercase segment`},
		{"asset_id mismatch", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,,,,,t,urn:openump:acme:berlin:press-05\n", "line 2: asset_id"},
		{"zero interval", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,0,,,,,t,\n", "line 2: temp: expected_interval_ms"},
		{"dictionary mismatch", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,,,urn:x:y,eclass,,\n", `line 2: temp: semantic_id "urn:x:y" does not have the form eclass uses`},
		{"unknown semantic form", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,,,AAB991,,,\n", "is not an ECLASS IRDI"},
		{"dictionary alone", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,,,,eclass,t,\n", "given without semantic_id"},
		{"control char tag", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,,,,,\"a\x01b\",\n", "source_tag must be at most 256"},
		{"area conflict", header + good + "acme,berlin,b,l,press-04,temp2,REAL,Cel,1000,,,,,t,\n", "line 3: urn:openump:acme:berlin:press-04 area/line b/l disagrees with line 2"},
		{"nothing to map", header + "acme,berlin,a,l,press-04,temp,REAL,Cel,1000,,,,,,\n", "has no semantic_id, source_tag or non-OpenUMP unit"},
		{"unterminated quote", header + "acme,berlin,a,l,press-04,\"temp,REAL,Cel,1000,,,,,,\n", "line 2:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ImportCSV(strings.NewReader(tc.csv), pressSettings, v)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	// Every problem is reported in one pass.
	_, err := ImportCSV(strings.NewReader(header+"acme,berlin,a,l,press-04,temp,REAL,degF,0,,,,,t,\n"), pressSettings, v)
	if d, ok := err.(Diagnostics); !ok || len(d) != 2 {
		t.Fatalf("want both unit and interval diagnostics, got %v", err)
	}
	// Excel's BOM is accepted.
	if _, err := ImportCSV(strings.NewReader("\xef\xbb\xbf"+header+good), pressSettings, v); err != nil {
		t.Fatal(err)
	}
}

func TestVendorEnvironmentImport(t *testing.T) {
	v := validator(t)
	raw, err := os.ReadFile(root + "/examples/aas/vendor-environment.json")
	if err != nil {
		t.Fatal(err)
	}
	o := AASOptions{Settings: pressSettings, Enterprise: "acme", Site: "berlin", Area: "utilities", Line: "coolant", Asset: "pump-02", ExpectedIntervalMS: 1000}
	cs, warnings := importAAS(t, raw, o, v)
	c := cs[0]
	if c.Metadata.AssetID != "urn:openump:acme:berlin:pump-02" || c.Metadata.DisplayName != "Coolant pump CP-200" {
		t.Fatalf("asset %+v", c.Metadata)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped 1 element") {
		t.Fatalf("warnings %v", warnings)
	}
	want := map[string]Measurement{
		"bearing_temperature": {DisplayName: "Bearing temperature", Type: "number", Unit: "Cel", ExpectedIntervalMS: 1000},
		"motor_current":       {DisplayName: "Motor current", Type: "number", Unit: "A", ExpectedIntervalMS: 1000, ValidRange: &ValidRange{0, 32}},
		"pump_running":        {DisplayName: "PumpRunning", Type: "boolean", Unit: "1", ExpectedIntervalMS: 1000},
	}
	if !reflect.DeepEqual(c.Metadata.Measurements, want) {
		t.Fatalf("measurements %+v", c.Metadata.Measurements)
	}
	if got := c.Mapping.Measurements["bearing_temperature"]; got != (MappedMeasurement{SemanticID: "0173-1#02-AAB991#008", Dictionary: "eclass", SourceUnit: "°C"}) {
		t.Fatalf("irdi: prefix not canonicalized or unit lineage lost: %+v", got)
	}
	if got := c.Mapping.Measurements["motor_current"]; got.Dictionary != "iec_cdd" {
		t.Fatalf("IEC CDD not recognized: %+v", got)
	}
	if c.Mapping.AAS == nil || c.Mapping.AAS.AASID != "https://vendor.example.com/ids/aas/CP-200-0042" || c.Mapping.AAS.SubmodelIDs.Nameplate == "" {
		t.Fatalf("binding %+v", c.Mapping.AAS)
	}
	// -submodels restricts the measurement source.
	o.Submodels = []string{"Nameplate"}
	if _, _, err := ImportAAS(bytes.NewReader(raw), o, v); err == nil || !strings.Contains(err.Error(), "no Property or Range") {
		t.Fatalf("nameplate is not a measurement source: %v", err)
	}
}

func TestAASImportRefusals(t *testing.T) {
	v := validator(t)
	c, _ := example(t, v)
	golden := exportBytes(t, c, Snapshots{}, v)
	mutate := func(f func(*Environment)) []byte {
		var env Environment
		if err := json.Unmarshal(golden, &env); err != nil {
			t.Fatal(err)
		}
		f(&env)
		raw, _ := Marshal(env)
		return raw
	}
	tech := func(env *Environment) *Element {
		var props []Element
		json.Unmarshal(env.Submodels[1].SubmodelElements[1].Value, &props)
		return &props[0]
	}
	setTech := func(env *Environment, e Element) {
		env.Submodels[1].SubmodelElements[1].Value = mustJSON([]Element{e})
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		o    AASOptions
		want string
	}{
		{"other site", golden, AASOptions{Settings: pressSettings, Enterprise: "acme", Site: "paris", Area: "a", Line: "l"}, "the shell records asset urn:openump:acme:berlin:press-04"},
		{"bad placement", golden, AASOptions{Settings: pressSettings, Enterprise: "ACME", Site: "berlin", Area: "a", Line: "l"}, `-enterprise "ACME"`},
		{"no unit", mutate(func(env *Environment) { env.ConceptDescriptions = nil }), pressPlacement, "no IEC 61360 unit"},
		{"unmappable unit", mutate(func(env *Environment) {
			env.ConceptDescriptions[0].EmbeddedDataSpecifications[0].DataSpecificationContent.Unit = "rpm"
		}), pressPlacement, `unit "rpm" has no OpenUMP v1.0 equivalent`},
		{"unknown valueType", mutate(func(env *Environment) {
			e := *tech(env)
			e.ValueType = "xs:dateTime"
			setTech(env, e)
		}), pressPlacement, `valueType "xs:dateTime"`},
		{"inverted range", mutate(func(env *Environment) {
			e := *tech(env)
			lo := "200"
			e.Min = &lo
			setTech(env, e)
		}), pressPlacement, "not a finite, ordered pair"},
		{"bad interval qualifier", mutate(func(env *Environment) {
			e := *tech(env)
			e.Qualifiers[0].Value = "soon"
			setTech(env, e)
		}), pressPlacement, `qualifier openump:expected_interval_ms "soon" is not an integer`},
		{"missing submodel", mutate(func(env *Environment) { env.Submodels = env.Submodels[:1] }), pressPlacement, "is referenced but not in the environment"},
		{"no shells", []byte(`{"submodels":[]}`), pressPlacement, "no assetAdministrationShells"},
		{"two shells, one -asset", mutate(func(env *Environment) {
			env.AssetAdministrationShells = append(env.AssetAdministrationShells, env.AssetAdministrationShells[0])
		}), AASOptions{Settings: pressSettings, Enterprise: "acme", Site: "berlin", Area: "a", Line: "l", Asset: "press-04"}, "-asset names one asset"},
		{"duplicate shell", mutate(func(env *Environment) {
			env.AssetAdministrationShells = append(env.AssetAdministrationShells, env.AssetAdministrationShells[0])
		}), pressPlacement, "already produced by"},
		{"trailing data", append(append([]byte{}, golden...), []byte("{}")...), pressPlacement, "trailing data"},
		{"trailing brace", append(append([]byte{}, golden...), '}'), pressPlacement, "trailing data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ImportAAS(bytes.NewReader(tc.raw), tc.o, v)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	// Collections nested beyond the bound are refused, not recursed.
	deep := Element{ModelType: "Property", IDShort: "x", ValueType: "xs:boolean"}
	for i := 0; i < maxCollectionDepth+2; i++ {
		deep = collection("c", []Element{deep})
	}
	raw := mutate(func(env *Environment) { env.Submodels[1].SubmodelElements[1].Value = mustJSON([]Element{deep}) })
	if _, _, err := ImportAAS(bytes.NewReader(raw), pressPlacement, v); err == nil || !strings.Contains(err.Error(), "nested deeper") {
		t.Fatalf("deep nesting: %v", err)
	}
	// Identifiers that are not URNs or URLs are warned about, not recorded.
	raw = mutate(func(env *Environment) { env.AssetAdministrationShells[0].ID = "press 04 shell" })
	cs, warnings, err := ImportAAS(bytes.NewReader(raw), pressPlacement, v)
	if err != nil || cs[0].Mapping.AAS.AASID != "" || len(warnings) != 1 {
		t.Fatalf("non-URI shell id: %v %v", err, warnings)
	}
}

func TestExportRefusesForeignSnapshots(t *testing.T) {
	v := validator(t)
	c, snap := example(t, v)
	telemetry := func(edit func(map[string]any)) map[string]any {
		raw, _ := os.ReadFile(root + "/examples/aas/telemetry.json")
		var m map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		d.Decode(&m)
		edit(m)
		return m
	}
	for _, tc := range []struct {
		name string
		snap Snapshots
		want string
	}{
		{"telemetry revision", Snapshots{Telemetry: telemetry(func(m map[string]any) { m["metadata_revision"] = json.Number("2") })}, "telemetry"},
		{"telemetry gateway", Snapshots{Telemetry: telemetry(func(m map[string]any) { m["source"].(map[string]any)["gateway_id"] = "other" })}, "acquisition source mismatch"},
		{"status asset", Snapshots{Status: map[string]any{"asset_id": "urn:openump:acme:berlin:pump-02", "metadata_revision": json.Number("1")}}, "status"},
		{"status revision", Snapshots{Status: map[string]any{"asset_id": c.Metadata.AssetID, "metadata_revision": json.Number("2")}}, "status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Export(c, tc.snap, v); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	// An unreconciled pair is refused before anything is projected.
	other := c
	other.Mapping.MetadataRevision = 2
	if _, err := Export(other, snap, v); err == nil {
		t.Fatal("mapping for another revision was exported")
	}
}

func TestNormalizeUnit(t *testing.T) {
	for in, want := range map[string]string{
		"°C": "Cel", "degC": "Cel", "Celsius": "Cel", "℃": "Cel", "Cel": "Cel", "": "1", "-": "1",
		"unitless": "1", "%": "%", "Percent": "%", "kPa": "kPa", "KPa": "kPa", "bar": "bar",
		"Volt": "V", "A": "A", "W": "W", "Hz": "Hz", "1/s": "Hz", "s": "s", "m": "m", "K": "K",
	} {
		if got, err := NormalizeUnit(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"C", "M", "degF", "rpm", "mm", "psi", "kW", "MPa", "l/min"} {
		if got, err := NormalizeUnit(in); err == nil {
			t.Errorf("%q mapped to %q; an ambiguous or foreign unit must be refused", in, got)
		}
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"MotorTemperature": "motor_temperature", "motor_temperature": "motor_temperature",
		"RPM": "rpm", "MaxRPMValue": "max_rpm_value", "Temp2Max": "temp2_max", "Motor_Temp": "motor_temp",
	} {
		if got := snake(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestMessageIDIsUUIDv7(t *testing.T) {
	at := time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC)
	id := messageID(at, "urn:openump:acme:berlin:press-04", 1)
	if id != messageID(at, "urn:openump:acme:berlin:press-04", 1) || id == messageID(at, "urn:openump:acme:berlin:press-04", 2) {
		t.Fatal("message id must be a function of asset, revision and time")
	}
	if id[14] != '7' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("%s is not a UUIDv7", id)
	}
}

func TestWriteRefusesOverwrite(t *testing.T) {
	v := validator(t)
	c, _ := example(t, v)
	other := c
	other.Metadata.Hierarchy.Asset = "pump-02"
	dir := t.TempDir()
	if _, err := WriteAll(dir, []Catalog{c}, false); err != nil {
		t.Fatal(err)
	}
	// One existing target refuses the whole set: nothing of pump-02 is written.
	if _, err := WriteAll(dir, []Catalog{other, c}, false); err == nil || !strings.Contains(err.Error(), "pass -force") {
		t.Fatalf("overwrite: %v", err)
	}
	if _, err := os.Stat(other.Paths(dir)[0]); !os.IsNotExist(err) {
		t.Fatal("a refused run left part of the set behind")
	}
	// The create itself never replaces a file that appeared after the check.
	if err := writeFile(c.Paths(dir)[0], []byte("{}"), false); err == nil || !strings.Contains(err.Error(), "pass -force") {
		t.Fatalf("no-clobber create: %v", err)
	}
	if _, err := WriteAll(dir, []Catalog{other, c}, true); err != nil {
		t.Fatal(err)
	}
}

// Review fixes: the round trip holds for opcua_companion and iec_cdd URI
// dictionaries, digit-leading assets, metadata without relationships and
// metadata with extensions; a BOM-prefixed AAS file imports.
func TestRoundTripEdgeCases(t *testing.T) {
	v := validator(t)
	c, _ := example(t, v)
	c.Metadata.Relationships = nil
	c.Metadata.Extensions = json.RawMessage(`{"acme.cell":"c-7"}`)
	c.Metadata.Hierarchy.Asset = "4press"
	c.Metadata.AssetID = "urn:openump:acme:berlin:4press"
	c.Mapping.AssetID = c.Metadata.AssetID
	c.Mapping.AAS = nil
	c.Mapping.SemanticID, c.Mapping.Dictionary = "http://opcfoundation.org/UA/Machinery/", "opcua_companion"
	c.Mapping.Measurements = map[string]MappedMeasurement{
		"motor_temperature": {SemanticID: "https://cdd.iec.ch/cdd/iec61360/iec61360.nsf/0112-2---61360_4%23AAE817", Dictionary: "iec_cdd"},
	}
	raw := exportBytes(t, c, Snapshots{}, v)
	if !bytes.Contains(raw, []byte(`"extensions"`)) {
		t.Fatal("export dropped the shell extensions")
	}
	cs, _ := importAAS(t, append([]byte("\xef\xbb\xbf"), raw...), pressPlacement, v)
	got := cs[0]
	if got.Metadata.AssetID != c.Metadata.AssetID {
		t.Fatalf("asset %s", got.Metadata.AssetID)
	}
	if got.Mapping.Dictionary != "opcua_companion" || got.Mapping.Measurements["motor_temperature"].Dictionary != "iec_cdd" {
		t.Fatalf("dictionaries lost: %+v", got.Mapping)
	}
	// Export accepts metadata without relationships and with extensions.
	meta, _ := marshal(c.Metadata)
	if bytes.Contains(meta, []byte("relationships")) || !bytes.Contains(meta, []byte(`"acme.cell": "c-7"`)) {
		t.Fatalf("metadata not carried losslessly:\n%s", meta)
	}
}

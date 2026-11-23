# Standards boundary: `hades import` and `hades aas export` (Hades-045)

OpenUMP v1.0 stays the edge wire contract ([ADR-0005](../decisions/0005-data-contract-and-standards-interoperability.md)).
Two boundary compilers connect it to the standards that plants and their customers use:

- **`hades import`** compiles a brownfield tag table (CSV) or an IDTA Asset Administration
  Shell (AAS V3.0 JSON) into a reviewed OpenUMP `metadata.json` and a
  `semantic-mapping.json` crosswalk;
- **`hades aas export`** projects a verified pair, with optional status and telemetry
  snapshots, into an AAS V3.0 JSON environment for a Digital Twin repository.

Neither command changes what runs on the broker. Every generated file passes the same
contract validation and reconciliation that `hades check` applies; an import that fails
any check writes nothing.

## The semantic-mapping contract

`urn:openump:schema:1.0:semantic-mapping` (`schemas/v1/semantic-mapping.schema.json`) is
an additive, optional contract. It is pinned to one metadata snapshot by
`(asset_id, metadata_revision)` and records, per measurement:

| Field | Meaning |
|---|---|
| `semantic_id` + `dictionary` | the dictionary concept: an ECLASS IRDI (`0173-1#02-AAB991#008`, `eclass`), an IEC CDD IRDI or URI (`iec_cdd`), an OPC UA companion specification URI (`opcua_companion`) or any URN/URL (`custom_uri`). The form must match the dictionary. |
| `source_tag` | the brownfield address the value came from (`DB102.DBD12`, `ns=2;s=Pump02.Current`, a historian tag, a Sparkplug metric) |
| `source_unit` | the unit spelling of the source when it differs from the OpenUMP unit (`°C` for `Cel`) |

A dictionary that the identifier form does not imply (`opcua_companion`, or `iec_cdd`
for a URI) is exported as an `openump:dictionary` qualifier, and as the
`ProductClassificationSystem` for the asset class, so it survives a round trip.

The asset can carry its own `semantic_id` (for example an ECLASS class) and an `aas`
block with the shell, global asset and submodel identifiers.

Reconcile a mapping with its snapshot:

```sh
hades check -kind semantic-mapping -file press-04.semantic-mapping.json -metadata press-04.metadata.json
```

A mapping for another asset or revision, or one naming a measurement the snapshot does
not declare, is refused. Site bundles signed before Hades-045 stay valid: the 24 prior
contract files are unchanged (`tests/conformance/baseline.json`) and a schema set without
`semantic-mapping` still compiles and validates.

## Import a tag table

One row per measurement; any number of assets. Example: [`examples/aas/tags.csv`](../../examples/aas/tags.csv).

```sh
hades import -from-csv tags.csv -out-dir catalog/ \
  -revision 3 -published-at 2026-11-23T10:00:00Z -gateway-id plc-01 -protocol s7
```

| Column | Required | Notes |
|---|---|---|
| `enterprise`, `site`, `area`, `line`, `asset` | yes | lowercase segments; `asset_id` is `urn:openump:{enterprise}:{site}:{asset}` |
| `measurement` | yes | `[a-z][a-z0-9_]{0,63}`, unique per asset |
| `type` | yes | `number`, `boolean`, `string`, or a PLC type (`REAL`, `LREAL`, `DINT`, `INT`, `WORD`, `BOOL`, `STRING`, …) |
| `unit` | yes (may be empty) | normalized to the OpenUMP enum, see below; empty means `1` |
| `expected_interval_ms` | yes | 1 to 86 400 000 |
| `min`, `max` | no | both or neither; numbers only |
| `semantic_id`, `dictionary` | no | the dictionary is inferred from the identifier form when absent |
| `source_tag` | no | |
| `display_name`, `asset_display_name`, `asset_type`, `asset_id` | no | `asset_id`, when given, must agree with the hierarchy |

The command line supplies what a tag table does not carry: `-revision`,
`-published-at` (default now; fix it for reproducible output), `-gateway-id`,
`-protocol` and `-status-interval-ms` (default 10000). A UTF-8 byte-order mark from a
spreadsheet export is accepted. Unknown columns are refused, so a typo is not silently
dropped.

Problems are reported together, each with its line, for example:

```text
line 4: hydraulic_pressure: unit "psi" has no OpenUMP v1.0 equivalent (accepted: 1 % Cel K bar Pa kPa m s Hz V A W)
line 7: door_closed: only a number measurement has a valid range
```

Output files are `<enterprise>.<site>.<asset>.metadata.json` and
`.semantic-mapping.json`. Unless `-force` is passed, the run is refused before anything
is written if any target exists, and each file is created without replacing one that
appeared in the meantime. The
`message_id` is a UUIDv7 derived from `published_at`, the asset and the revision, so the
same input compiles to byte-identical files.

### Units

OpenUMP v1.0 has a closed unit set: `1 % Cel K bar Pa kPa m s Hz V A W`. Imports accept
these and common spellings (`°C`, `degC`, `Celsius`, `percent`, `Volt`, `KPa`, `1/s`,
`unitless`, …). Ambiguous or foreign units (`C`, `degF`, `rpm`, `psi`, `mm`, `kW`) are
refused, never converted: a converted value would no longer match the PLC. The original
spelling is kept as `source_unit`.

## Import an AAS

```sh
hades import -from-aas vendor-environment.json -out-dir catalog/ \
  -enterprise acme -site berlin -area utilities -line coolant [-asset pump-02] \
  -revision 1 -gateway-id gw-01 -protocol opcua -expected-interval-ms 1000
```

Input is an **AAS V3.0 JSON environment**. `.aasx` packages are not read (see
[deviations](#deviations)); extract the environment JSON from the package first.

- Each shell becomes one asset. The asset segment comes from the shell's `idShort`
  (lowercased, `_` to `-`, Hades' `asset_` prefix for digit-leading segments removed) unless `-asset` names it (one shell only). The ISA-95 placement
  comes from flags; a shell exported by Hades records its `asset_id`, and a placement that
  disagrees with it is refused.
- `Property` and `Range` elements become measurements, including inside
  `SubmodelElementCollection`s (at most 8 deep). Other element types are skipped with a
  warning. `idShort`s are converted to measurement names (`MotorCurrent` to `motor_current`).
- Digital Nameplate submodels are static data, not measurements. In a Technical Data
  submodel only `TechnicalProperties` is read; the `ProductClassifications` class becomes
  the asset `semantic_id`. `-submodels` restricts which submodels are read.
- `valueType` gives the type (`xs:boolean`, `xs:string`, any numeric XSD type).
- The unit comes from the IEC 61360 content of the element's concept description, found
  through its `semanticId` or a supplemental semantic id. **A numeric element without a
  unit is refused**: unit `1` must be stated, not assumed.
- An `ExternalReference` `semanticId` that is an ECLASS IRDI (with or without `irdi:`), an
  IEC IRDI or a URN/URL becomes the measurement `semantic_id`.
- The interval comes from an `openump:expected_interval_ms` qualifier, else from
  `-expected-interval-ms`.
- Shell, global asset and submodel identifiers that are URNs or URLs are recorded in the
  mapping's `aas` block; others are reported as warnings and not recorded.

## Export to AAS

```sh
hades aas export -metadata press-04.metadata.json -mapping press-04.semantic-mapping.json \
  [-status status.json] [-telemetry telemetry.json] -out press-04.aas.json
```

The output ([`examples/aas/press-04.aas.json`](../../examples/aas/press-04.aas.json)) is
deterministic and holds:

| Part | Content |
|---|---|
| Shell | `id` from the mapping (default `urn:openump:aas:{enterprise}:{site}:{asset}`), `globalAssetId` (default the `asset_id`), `assetType` `urn:openump:asset-type:{asset_type}`, extensions `openump:asset_id` and `openump:metadata_revision` |
| `Nameplate` | semanticId IDTA 02006 `https://admin-shell.io/zvei/nameplate/2/0/Nameplate`; `ManufacturerProductDesignation` from `display_name` |
| `TechnicalData` | semanticId IDTA 02003 `https://admin-shell.io/ZVEI/TechnicalData/Submodel/1/2`; `ProductClassifications` with the asset `semantic_id`; `TechnicalProperties` with one `Range` (when a valid range exists) or `Property` per measurement, carrying `semanticId`, display name and qualifiers for the interval, source tag and source unit |
| `OperationalData` | only with `-status` or `-telemetry`: producer and acquisition state, and each measurement's latest value with `openump:quality` and `openump:observed_at` qualifiers |
| Concept descriptions | one per measurement, `urn:openump:concept:{enterprise}:{site}:{asset}:{measurement}`, with IEC 61360 preferred name, unit (the OpenUMP code) and data type, and `isCaseOf` the dictionary concept |

A measurement with a dictionary `semantic_id` uses it as the element `semanticId` and
links its own concept description as a supplemental semantic id; two measurements can
share one dictionary concept without sharing a unit.

Snapshots must belong to the exported catalog: telemetry and status for another asset or
metadata revision, or telemetry from another gateway, are refused.

The export imports back to the same catalog (`internal/aas` round-trip tests). It is
validated against the official AAS V3.0.6 JSON schema, vendored unmodified with
attribution in `tests/fixtures/aas/`, by `tests/test_aas_export.py`.

## Deviations

These are deliberate limits of Hades-045:

- **No `.aasx` input.** ADR-0005 names AAS JSON and `.aasx` packages; this release reads
  the JSON environment only. Reading an OPC package (ZIP with relationship parts and
  supplementary files) is deferred.
- **Template-referenced, not template-complete.** The `Nameplate` and `TechnicalData`
  submodels carry the IDTA template semanticIds and only the elements Hades knows. They
  are metamodel-valid but are not complete template instances: Nameplate mandatory
  elements such as `ManufacturerName`, `SerialNumber`, `YearOfConstruction` and the
  address are absent, because OpenUMP metadata does not hold them. Element-level template
  semanticIds are not emitted.
- **No IDTA 02008 Time Series.** `OperationalData` holds the latest value per measurement
  under a Hades semanticId (`urn:openump:aas:submodel:operational-data:1.0`). It is not a
  Time Series submodel and does not claim to be one.
- **Metadata relationships and extensions are not projected.** They are validated with
  the snapshot but have no place in the export, so an import from AAS has none.
- **No unit conversion.** Only spellings of the 13 OpenUMP units are accepted.
- **Go cannot validate the AAS schema.** Its RE2 engine rejects the schema's surrogate
  escapes, so the official-schema check runs in the Python test suite. The Go exporter is
  tested against the committed golden file.

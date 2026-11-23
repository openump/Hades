# ADR-0005: Canonical edge wire contract and standards interoperability (AAS, ECLASS, Sparkplug B)

**Status:** Proposed Hades design (`Hades-045`)

**Date:** 17 November 2026

## Context

Enterprise customers and system integrators evaluating Hades operate in heterogeneous brownfield and regulatory environments:

1. **European Industry 4.0 and Digital Product Passport (DPP) ecosystems** mandate the **IDTA Asset Administration Shell (AAS V3 / IEC 63278)** with dictionary references (`semanticId`) pointing to **ECLASS** (IRDI) or **IEC 61360 Common Data Dictionary (CDD)**.
2. **North American and global SCADA/MES deployments** frequently use **Eclipse Sparkplug B** (`spBv1.0`) with Ignition, Kepware (`KEPServerEX`), or HighByte, alongside legacy PLC tag tables exported from Siemens TIA Portal, Step 7, or Rockwell Studio 5000.
3. **Machine builders** expose domain models via **OPC UA Companion Specifications** (e.g., PackML, umati / VDMA machine tools, Euromap).

A recurring architectural question is whether Hades should replace its OpenUMP v1.0 wire contracts (`schemas/v1/telemetry.schema.json`, `metadata.schema.json`, `status.schema.json`) with raw AAS V3 JSON or Sparkplug B payloads, or whether it should retain OpenUMP v1.0 as the canonical edge runtime contract and bridge to external standards at the onboarding and northbound boundaries.

Furthermore, OpenUMP v1.0 enforces cryptographic baseline hash pinning across all published contract files (`Hades-006`, `Hades-035`, `Hades-043`) so that deployed, Ed25519-signed site bundles remain valid across additive upgrades. Directly mutating `schemas/v1/metadata.schema.json` to add new fields or unit enums would alter its baseline SHA-256 digest and invalidate existing signed site bundles.

## Decision

1. **Retain OpenUMP v1.0 as the canonical edge runtime wire contract.**
   All edge ingress, normalization (`hades-normalizer`), MQTT UNS publication (`openump/v1/{enterprise}/{site}/{area}/{line}/{asset}/{message_type}`), local history (`hades-history`), edge inference (`hades-infer`), 5-gate actuation (`hades-actuator`), and cloud transfer (`hades-transfer`) continue to use the flat, deterministic OpenUMP v1.0 JSON contracts without modification to existing schema hashes.

2. **Introduce an additive semantic crosswalk contract (`urn:openump:schema:1.0:semantic-mapping`).**
   Rather than mutating `schemas/v1/metadata.schema.json` (which would break signed site bundle compatibility), define an optional, additive companion schema `schemas/v1/semantic-mapping.schema.json` pinned to `(asset_id, metadata_revision)`. For each asset and declared measurement, `semantic-mapping` records:
   - `semantic_id`: standard dictionary concept identifier (ECLASS IRDI such as `0173-1#02-AAB991#008`, IEC CDD URI, or OPC UA Companion Spec `NodeId` / URI);
   - `aas`: optional IDTA AAS V3 `assetAdministrationShellId`, `globalAssetId`, and target submodel/element path bindings (IDTA 02006 Digital Nameplate, IDTA 02003 Technical Data, IDTA 02008 Time Series);
   - `source_lineage`: legacy brownfield PLC/SCADA tag address (e.g., `DB102.DBD12`), historian tag name, or Sparkplug B metric alias used during migration.

3. **Provide deterministic boundary compilers (`hades import` and `hades aas`) for zero-friction onboarding and Digital Twin projection.**
   - **Southbound / Brownfield Onboarding (`hades import`)**: Compile vendor AAS V3 JSON / `.aasx` packages and brownfield PLC/SCADA tag exports (Siemens TIA Portal CSV, Kepware CSV/JSON) into a reviewed OpenUMP `metadata.json` + `semantic-mapping.json` pair.
   - **Northbound / Digital Twin Projection (`hades aas`)**: Project verified OpenUMP `(metadata.json, semantic-mapping.json)` catalogs and live/retained `status` + `telemetry` snapshots into standard IDTA AAS V3 JSON (`Environment` / `Submodel` documents) for enterprise Digital Twin repositories and `Zeus`.

## Alternatives and consequences

### Alternative A: Replace OpenUMP wire schemas with raw AAS V3 JSON on the edge broker
* **Rejected.** AAS V3 metamodels (`SubmodelElementCollection`, nested `Property` descriptors, `semanticId.keys[]`, `qualifiers[]`, `valueType`) are designed for lifecycle asset repositories and REST APIs, not high-frequency deterministic edge pub/sub. Streaming PLC telemetry as AAS V3 JSON increases payload size by 4x–7x and adds allocation pressure against the `<250 MB` Tier 2 IPC budget. More importantly, AAS V3 lacks native constructs for `(asset_id, metadata_revision)` quarantine reconciliation, `clock_origin` / `clock_uncertainty_ms` bounds, MQTT session-fenced last wills, and 5-gate Ed25519 actuation receipts.

### Alternative B: Replace OpenUMP wire schemas with Eclipse Sparkplug B
* **Rejected.** Sparkplug B flattens equipment hierarchy into `spBv1.0/{group_id}/{message_type}/{edge_node_id}/{device_id}`, losing the explicit 5-level ISA-95 path (`enterprise/site/area/line/asset`) enforced by OpenUMP topic ACLs. Its `NCMD`/`DCMD` command channel provides no cryptographic signature verification, safety envelope check, or local confirmation gate. Sparkplug B is appropriate as an external brownfield adapter, not as Hades's safety-critical internal contract.

### Alternative C: Mutate `schemas/v1/metadata.schema.json` in place to add `semantic_id`
* **Rejected for v1.0.** Because `schemas/v1/metadata.schema.json` enforces `"additionalProperties": false` and its SHA-256 digest is pinned in the contract baseline, editing it in place would invalidate existing signed site bundles (`Hades-020`). Using the existing bounded `extensions` map in `metadata.schema.json` for lightweight inline tags while providing `urn:openump:schema:1.0:semantic-mapping` for full AAS/ECLASS/brownfield crosswalks preserves 100% backward compatibility with all 24 existing baseline entries.

## Verification and implementation gates

`Hades-045` implements and verifies the additive `semantic-mapping.schema.json` contract, independent Python/Go contract oracle fixtures, brownfield CSV and AAS V3 import into OpenUMP `metadata.json` + `semantic-mapping.json`, and deterministic AAS V3 projection without altering any existing baseline contract hash.

## Sources

- [IDTA Asset Administration Shell (AAS) Specification Part 1: Metamodel V3.0 (IEC 63278-1)](https://industrialdigitaltwin.org/en/content-hub/aasspecifications)
- [ECLASS IRDI (ISO 29002-5 / IEC 61360) identifier syntax](https://eclass.eu/en/support/technical-specification/structure-and-elements/irdi)
- [ISA-95 / IEC 62264 Enterprise-Control System Integration](https://www.isa.org/standards-and-publications/isa-standards/isa-standards-committees/isa95)
- [Eclipse Sparkplug Specification v3.0.0](https://sparkplug.eclipse.org/specification/)

## Tracking

[Hades-045](https://github.com/openump/Hermes/issues/50)

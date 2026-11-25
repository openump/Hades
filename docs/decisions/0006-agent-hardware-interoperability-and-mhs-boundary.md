# ADR-0006: Agent hardware interoperability and Model Hardware Standard (MHS) boundary

**Status:** Accepted; planned for Milestone M7 (`Hades-047`, `Hades-048`, `Hades-049`). Future MHS wire bridges remain deferred until Anthropic publishes a versioned open-source specification.

**Date:** 25 November 2026

## Context

On 27 August 2026, Anthropic and the HHMI Janelia Research Campus announced a closed research preview of the **Model Hardware Standard (MHS)**: a model-agnostic driver and metadata pattern enabling AI agents to discover, inspect, and operate physical instruments and manufacturing equipment through uniform `read`/`write` primitives, self-describing capability/limits reference files, driver-level safety boundaries below the agent, and tri-modal control paths (MCP, CLI, and code/APIs).

Comparing MHS against Hades's implemented baseline (`Hades-001`–`Hades-046`) highlights two structural realities:

1. **Hades already provides stronger industrial safety and durability primitives than lab-bench MHS.** Hades separates read-only OT acquisition (`internal/gateway`, scan-cycle coalescing/rate/circuit-breaker guardrails) from a cryptographic, five-gate write-control plane (`hades-actuator` with Ed25519/JCS signatures, TTL leases, replay-proof hash-chained audit WAL, `L0` shadow default, `L1` local `control.sock` operator confirmation, persistent `shadow.lock` kill switch, and PLC-owned staging interlocks/watchdog fallback), alongside local numeric history, offline segmented WAL delivery, and EU CRA / IEC 62443-4-2 evidence.
2. **Hades still has three concrete usability gaps for AI and coding agents:**
   - **Fragmented and hidden write limits:** Asset truth is split across `metadata.json`, `semantic-mapping.json`, and `actuation-policy.json`. `hades-mcp` (`get_asset_context`) currently returns `metadata.json` only; an authorized proposer agent cannot discover writable parameters, envelopes (`min_value`, `max_value`), `max_delta_per_step`, `cooldown_seconds`, `required_preconditions`, or effective autonomy ceilings until `propose_command` fails. Nor does Hades have a bounded schema for natural-language physical properties and operational troubleshooting notes (`common#/$defs/extensions` is capped at 16 flat keys of 256 characters each).
   - **No agent-ergonomic CLI surface:** `hades-mcp` supports MCP over stdio and loopback mTLS HTTP, while coding agents and scripts frequently compose CLI commands. Adding a second direct MQTT/signing client would duplicate auth, rate-limiting, `L1` capping, and key-handling risks.
   - **Manual OPC UA onboarding:** MHS demonstrates compressing hardware setup via device discovery. In OT environments, auto-admitting discovered endpoints into production is unacceptable, yet operators still need staged, read-only discovery that emits reviewable Git-managed drafts.

At the same time, MHS has **no public, frozen open-source wire specification** as of November 2026, and 95%+ of brownfield manufacturing controllers (OPC UA, Modbus TCP, Siemens S7, EtherNet/IP, ADS, BACnet) will not run native MHS drivers. Furthermore, Hades enforces cryptographic baseline hash pinning across all 28 contract inventory entries (11 versioned schemas in `tests/conformance/baseline.json`) so deployed signed site bundles stay valid across additive upgrades.

## Decision

1. **Retain OpenUMP v1.0 as the canonical edge wire contract and preserve all 28 prior baseline hashes.**
   Extending [ADR-0005](0005-data-contract-and-standards-interoperability.md), Hades will not replace `telemetry`, `metadata`, `status`, `command`, `actuation-policy`, or `command-receipt` schemas with MHS formats. All MHS-inspired capabilities and future MHS wire bridges are strictly additive boundary contracts and adapters.

2. **Introduce an additive `hardware-profile` contract and deterministic Hardware Reference Card compiler (`Hades-047`).**
   - Define `schemas/v1/hardware-profile.schema.json` (`urn:openump:schema:1.0:hardware-profile`) pinned to `(asset_id, metadata_revision)`, matching the reconciliation pattern of `semantic-mapping.schema.json` (`Hades-045`) and the 65,536-byte payload bound. It carries strictly length-bounded, control-character-free and bidi-override-free natural-language context: physical attributes, operating notes, hazard/safety notes, troubleshooting hints, and per-measurement/per-parameter descriptions. All text is explicitly typed and rendered as **non-executable descriptive context** (never policy or instructions).
   - Add `hades describe` to deterministically compile `(metadata.json, optional semantic-mapping.json, optional hardware-profile.json, optional actuation-policy.json, optional status.json)` into a canonical **Hardware Reference Card** in structured JSON (`-format json`) or LLM/human-readable Markdown (`-format md`).
   - Extend `hades-mcp` with optional comma-separated `-semantic-mapping` and `-hardware-profile` catalog inputs, a read-only `get_asset_capabilities` tool, and per-asset MCP Resources (`openump://assets/<enterprise>/<site>/<asset>/reference.md`, registered for `resources/list` discovery and `resources/read` retrieval without URN colon percent-encoding ambiguity).

3. **Enforce strict role-scoped capability sanitization across all agent views (`Hades-047`).**
   - **Universal redaction (all roles and offline `hades describe`):** Strip internal PLC mailbox addresses (`holding:`, `db:`, `ua:`), PLC/fieldbus endpoints, `allowed_origins`, `key_id` values, signing material, and broker ACL internals.
   - **Role-scoped disclosure in `hades-mcp`:**
     - `mcp-readonly` receives `metadata` + `semantic-mapping` + `hardware-profile` + live status freshness only, and reports actuation as disabled/unavailable. Read-only deployments continue to require no `-policy` file and leak zero information about which PLC parameters are writable or what preconditions gate them.
     - `mcp-proposer` additionally receives the sanitized writable-parameter contract for assets present in `-policy`: allowlisted parameter names, units, hard `min_value`/`max_value`, `max_delta_per_step`, `cooldown_seconds`, `required_preconditions`, and effective autonomy ceiling `min(site_autonomy_level, asset.autonomy_level, L1)`—explicitly stating that `hades-mcp` caps AI proposals at `L1` and `hades-actuator` plus PLC interlocks enforce all gates. Assets not listed in `-policy` cleanly report actuation as disabled.

4. **Implement `hades hw` strictly as a thin TLS 1.3 mTLS client to `hades-mcp` (`Hades-048`).**
   - Provide `hades hw list | describe | read | status | health | history | propose` speaking MCP Streamable HTTP (`DisableStandaloneSSE: true` for short-lived CLI calls) over TLS 1.3 mTLS to the running `hades-mcp -listen` loopback endpoint.
   - Preserve a **single enforcement boundary**: `hades-mcp` remains the sole owner of certificate-CN role mapping, session-to-CN binding, per-caller rate limiting, `L1` autonomy capping, bounded PromQL construction, Ed25519 command signing, and receipt verification. A coding agent invoking `hades hw` holds only its scoped HTTP client certificate and never holds MQTT broker credentials or command-signing private keys.
   - `hades hw propose` defaults fail-safe to `L0` shadow evaluation (`dry_run: true`); queuing an `L1` operator confirmation requires the explicit `-queue-l1` opt-in flag. Offline compilation without a live daemon remains in `hades describe`.

5. **Implement staged, read-only OPC UA discovery (`hades discover opcua`) emitting reviewable draft bundles (`Hades-049`).**
   - Reuse the pinned `awcullen/opcua` client (`SignAndEncrypt`, pinned server certificate, X.509 read-only identity; no silent downgrade to `None`/anonymous) with bounded, rate-limited BFS browsing (`-max-depth`, `-max-nodes`, `-timeout`, `-rate`, explicit namespace URI allowlist) to protect PLC scan cycles.
   - Emit a reviewable draft bundle into `-out-dir` (no-clobber unless `-force`, refusing to target an active `site.json` runtime directory) using explicit ISA-95 placement flags: `metadata.draft.json`, `semantic-mapping.draft.json`, `hardware-profile.draft.json`, and `discovery-report.json`.
   - Never fabricate or guess units: map OPC UA `EngineeringUnits` (standard UNECE Rec 20 `UnitID` / canonical display names, or an explicit operator-supplied `-unit-map`) to the closed OpenUMP v1.0 UCUM unit set; omit unmappable or non-scalar nodes into `discovery-report.json` with explicit reasons (and emit no invalid `metadata.draft.json` if zero valid measurements remain).
   - Never emit `actuation-policy.json`: OPC UA nodes with write `AccessLevel` bits are read-only observations in `metadata.draft.json` and listed solely as `candidate_writable_nodes` in `discovery-report.json` for human engineering review.

6. **Reserve the future bidirectional MHS wire-adapter boundary under strict Hades invariants (deferred until public MHS spec release).**
   - **Northbound (`hades-mhs-bridge`):** Once Anthropic publishes a versioned open-source MHS specification, Hades may project approved brownfield OT assets as MHS virtual devices where MHS `read` maps to the validated UNS `LatestCache` / `hades-history-proxy` (zero direct PLC polling) and MHS `write` translates into a signed OpenUMP `command` routed through `hades-actuator`'s five gates, capped at `L0`/`L1` for AI/agent origins identically to `hades-mcp`.
   - **Southbound (`mhs-driver`):** To ingest greenfield MHS-native hardware (cobots, cameras, inline QA lab instruments) into the OpenUMP UNS, third-party or Python-only MHS vendor drivers must run read-only inside isolated, supervised sidecars communicating with Hades over a private local Unix domain socket (matching the Telegraf sidecar pattern in `internal/gateway/driver.go` and preserving pure-Go core services per [ADR-0003](0003-runtime-language.md)).
   - **Gate:** No MHS wire-adapter implementation ticket opens until an open-source, versioned MHS specification exists and its license is cleared under [ADR-0001](0001-license-policy.md).

## Alternatives and consequences

### Alternative A: Adopt MHS directly as Hades's internal edge contract
* **Rejected.** MHS is an unpublished research preview oriented around device-level agent interaction; it does not provide ISA-95 UNS topic governance, `clock_uncertainty_ms`, segmented WAL offline replay ([ADR-0004](0004-delivery-durability.md)), cryptographic command receipts, or EU CRA compliance. Keeping OpenUMP v1.0 canonical lets Hades deliver immediate value with zero spec-churn risk.

### Alternative B: Store physical context in `metadata.schema.json` or `metadata.extensions`
* **Rejected.** Editing `metadata.schema.json` mutates its pinned baseline SHA-256 digest and invalidates deployed signed site bundles. Using `metadata.extensions` hits its hard schema ceiling (16 flat keys, 256-char string values) and cannot cleanly express structured per-measurement descriptions, physical properties, and hazard/troubleshooting notes.

### Alternative C: Expose sanitized `actuation-policy` bounds to `mcp-readonly`
* **Rejected.** Disclosing writable parameter allowlists, thresholds, and precondition structures to read-only identities widens reconnaissance surface and breaks the strict separation where read-only `hades-mcp` deployments never mount `-policy`. Advisory workflows that require write-bound awareness can be provisioned with a dedicated `mcp-proposer` identity limited to `L0` shadow policy on the site.

### Alternative D: Build `hades hw` as a direct MQTT/history/signing CLI
* **Rejected.** A direct client would duplicate `hades-mcp`'s rate limits, PromQL bounds, `L1` cap, and receipt verification, miss non-retained live telemetry between CLI invocations, and place broker credentials and Ed25519 command-signing keys directly inside the calling agent's shell environment.

### Alternative E: Allow `hades discover` to emit `actuation-policy.draft.json` or write directly into site config
* **Rejected.** Auto-generating write policies from PLC `AccessLevel` flags risks rubber-stamping unsafe envelopes, missing interlocks, or exposing direct outputs. Discovery must stay strictly read-only and draft-isolated until reviewed in Git and validated with `hades check`.

### Alternative F: Embed Python MHS vendor SDKs into Hades services
* **Rejected.** Violates [ADR-0003](0003-runtime-language.md) and enlarges the attack surface and memory footprint against the Tier 2 IPC budget. Supervised Unix-socket sidecars maintain crash isolation, digest verification, and Go-native core binaries.

## Verification and implementation gates

Milestone M7 executes this decision in three ordered tickets tracked in [openump/Hermes](https://github.com/openump/Hermes):

- **`Hades-047`**: Additive `hardware-profile.schema.json` (preserving all 28 prior baseline hashes; Go + Python dual-oracle conformance), deterministic `hades describe` Reference Card compiler, and role-scoped `hades-mcp` `get_asset_capabilities` tool + `openump://assets/<enterprise>/<site>/<asset>/reference.md` MCP resources.
- **`Hades-048`**: Thin TLS 1.3 mTLS `hades hw` CLI talking to `hades-mcp -listen`, verified against a real supervised `hades-mcp` + `hades-actuator` + mTLS broker stack (including `mcp-readonly` refusal and `L0`/`L1` signed receipts).
- **`Hades-049`**: Staged, bounded, read-only `hades discover opcua` verified against `hades-opcua-mock` (proving `Private` node omission, unmappable-unit reporting, hostile-string handling, zero write-policy generation, and `hades check` acceptance of emitted drafts).

## Sources

- [Anthropic — Model Hardware Standard Research Preview (27 August 2026)](https://www.anthropic.com/news/model-hardware-standard-research-preview)
- [Model Context Protocol (MCP) Specification](https://modelcontextprotocol.io/specification)
- [ADR-0001: License policy](0001-license-policy.md)
- [ADR-0003: Runtime language](0003-runtime-language.md)
- [ADR-0004: Delivery durability](0004-delivery-durability.md)
- [ADR-0005: Canonical edge wire contract and standards interoperability](0005-data-contract-and-standards-interoperability.md)
- [Write-control contract profile (`Hades-032`)](../contracts/write-control-plane.md)
- [Industrial MCP server operations (`Hades-034`)](../operations/industrial-mcp.md)

## Tracking

- [Hades-047](https://github.com/openump/Hermes/issues/52)
- [Hades-048](https://github.com/openump/Hermes/issues/53)
- [Hades-049](https://github.com/openump/Hermes/issues/54)

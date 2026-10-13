# OpenUMP v1.0 metadata and status

## Shared validation

Metadata and status use UTF-8 objects of at most 65,536 bytes, strict JSON parsing, exact `schema_version: 1.0`, lowercase UUID message IDs, canonical millisecond UTC timestamps, and the closed-field/extension policy in [telemetry.md](telemetry.md). The schemas are [metadata](../../schemas/v1/metadata.schema.json) and [status](../../schemas/v1/status.schema.json). Format assertions and local reference resolution are required.

These are asset-scoped messages. A gateway collecting multiple assets publishes one metadata/status stream per asset; it does not report a whole gateway as a single machine. Credentials determine allowed publication scope, independently of schema validity.

## Asset metadata

Required fields are schema version, message ID, asset ID, metadata revision, publication time, display name, equipment type, hierarchy, acquisition definition, and measurement definitions. Hierarchy gives the current enterprise/site/area/line/asset path; enterprise must agree with immutable identity ownership, but current site may differ from the allocation site after relocation.

Revisions are positive integers that increase for every accepted asset-context change, including relocation, acquisition identity, measurement definitions, and relationships. A revision is immutable. Identical content may be republished; changed content at the same revision is a conflict. Keep prior revisions for the agreed replay/history window. Publication time is not a substitute for revision ordering.

Acquisition identifies the configured gateway, protocol, and expected status interval in milliseconds. Measurement definitions fix the display name, type (`number`, `boolean`, or `string`), supported UCUM unit, and expected acquisition interval. Boolean/string definitions use unit `1`. Intervals are positive integers up to one day. Optional numeric ranges contain inclusive minimum/maximum and must not be inverted. Ranges describe acceptable data semantics, not safety interlocks or automatic operating thresholds.

Optional relationships contain relation name, target asset ID, an effective-from time, an optional exclusive effective-until time, and provenance. Provenance names an engineering authority, source record/revision, verifying person or role, and verification time. A source reference is an identifier for an authoritative record, not proof that the record has been authenticated. Consumers/deployments must enforce that authority. Model-generated hypotheses must never masquerade as these relationships.

The initial profile forbids self-relations, duplicate relation/target pairs in a snapshot, nonpositive effective periods, and cross-enterprise relationships. A target may be temporarily unavailable in the local registry; consumers report unresolved context rather than create a fictional asset. Relationships are applicable only within their effective interval.

## Reconciliation with observations

Resolve `(asset_id, metadata_revision)` exactly. If that revision is missing, hold the observation in a bounded pending/quarantine path and report missing context; never interpret it using the latest revision by default. Hades-007 specifies buffer capacity, persistence, and loss policy.

Check that the observation's gateway/protocol and measurement names/types/units agree with the snapshot. Do not treat Python/JSON booleans as numeric measurements. Null readings remain bad-quality observations. A good-quality value outside its declared range is invalid; diagnostic values outside the range may be preserved with non-good quality. Different source times or old revisions are not automatically errors during historical replay.

The trusted current registry is used for current publication topic matching. Historical replay uses the applicable historical mapping. Retained metadata supplies the latest revision but is not an archive of all revisions; the runtime must persist historical snapshots separately if replay depends on them.

## Producer status

Required status fields include message identity, asset identity, metadata revision, `reported_at`, `kind`, source identity, producer state, acquisition state, `last_observed_at`, and `valid_for_ms`. Source identity contains gateway ID, boot UUID, and connection-session UUID. A new process boot gets a new boot UUID and every new MQTT connection gets a fresh session UUID.

| Field | Values and meaning |
| --- | --- |
| `kind` | `heartbeat`, `shutdown`, or `last_will`. |
| `producer_state` | `online`, `degraded`, or `offline`; producer availability only. |
| `acquisition_state` | `healthy`, `degraded`, or `unknown`; acquisition assessment, not equipment operating state. |
| `last_observed_at` | Last actual acquisition time or null when no trustworthy time is known. A healthy acquisition requires a timestamp. |
| `valid_for_ms` | Positive duration up to one day after which this status cannot be assumed current. |

Heartbeat messages may report online/degraded producer state. Shutdown and last-will messages must report offline producer state and unknown acquisition state. A last will must set `last_observed_at` to null because its preconfigured body cannot know the final acquisition time. Optional reasons provide bounded diagnostics without credentials.

`reported_at` for a heartbeat/shutdown is authoring time. For a last will it is the time the will was prepared before connecting, not disconnect detection time. A consumer must not report that timestamp as the machine failure time. Track a separate trusted transport receipt/detection time where available.

## MQTT lifecycle and retention

Use MQTT 5 for the eventual reference profile. Publish metadata and latest status retained at QoS 1; publish telemetry at QoS 1 without retention. The capability probe's retained test message is unrelated to this deployment rule.

Configure an asset-scoped retained offline last will before connection and publish a fresh online heartbeat after successful startup. Configure zero Will Delay for the initial profile; Keep Alive still bounds disconnect detection and delivery is not instantaneous. Graceful shutdown explicitly publishes shutdown status before normal disconnect. Each will gets a distinct message ID that is not reused for heartbeat messages.

Set heartbeat MQTT Message Expiry Interval to `ceil(valid_for_ms / 1000)` seconds, and choose `valid_for_ms` no greater than three times the configured expected status interval. This relationship requires metadata and is not enforced by the structural status schema. Metadata is retained without automatic expiry until revised/decommissioned. Telemetry is not retained; delivery expiry is a separate Hades-007 decision. Clear obsolete retained topics through authorized zero-length retained deletion operations when an asset moves or is retired.

For last wills, MQTT expiry begins when the broker publishes the will. A live consumer may use its trusted receipt time to bound status freshness. A new subscriber cannot infer the publication age merely from the old body timestamp or a reset local receipt time; use trustworthy remaining-expiry/broker context if available, otherwise treat current age as unknown. Duplicate delivery must not reset freshness or turn an old observation into a new one.

Session UUIDs are opaque and cannot order sessions. A late will from a superseded connection must not override a known active session. If producer-session ownership cannot be established and conflicting session messages arrive, report unknown availability until authoritative session reconciliation; do not order UUIDs or assume the latest arrival is correct. The runtime's producer identity/session management and receiver reconciliation must be verified in the broker and failure tickets.

See the [MQTT 5 standard](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html) for Will, Message Expiry, retained-message, and Keep Alive transport behavior. OpenUMP's constraints above are application-profile choices.

## Observation freshness

For a known clock uncertainty estimate `U`, a reading is stale when its age exceeds `3 × expected_interval_ms + U`. If it is further in the future than `U`, or uncertainty is unknown, freshness is unknown. Otherwise it is fresh. The consumer clock must also be trusted; combine consumer and source uncertainty in `U` where relevant. Evaluate per measurement using its declared interval, not solely the last received message time.

Quality and freshness are independent. A fresh bad reading is not usable good data; an old good reading is stale. A fresh healthy status cannot make stale telemetry current and cannot establish that a machine is running. The foundation freshness helper implements this initial rule for already-validated times and metadata.

## Verification

Context fixtures exercise revisions, relationship provenance, effective periods, relocated metadata, value definitions, heartbeats, shutdown, and preconfigured wills. Tests also check exact metadata reconciliation, type/unit agreement, ranges, and stale/future/unknown-clock readings. Run the README test command. These tests specify and verify contract behavior; no MQTT lifecycle implementation is delivered by Hades-005.

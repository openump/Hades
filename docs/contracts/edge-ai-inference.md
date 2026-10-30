# OpenUMP Edge AI inference contract profile (Hades-035)

This profile defines two versioned contracts. A **model manifest** describes a signed
Edge AI model or skill package. An **inference event** is a structured anomaly or
classification output. Both are specification and offline validation only. Hades-036
implements the runtime (`hades-infer`): signature and digest verification, sliding-window
inference, budgets and rollback.

## Contracts and validation boundaries

| Contract | Schema ID | Topic |
|---|---|---|
| Model manifest | `urn:openump:schema:1.0:model-manifest` | Not an MQTT payload; distributed as a signed package (Zeus-003). |
| Inference event | `urn:openump:schema:1.0:inference-event` | `openump/v1/<enterprise>/<site>/<area>/<line>/<asset>/event/<inference_type>` |

Validation runs in three layers, matching the control profile. Rejections carry
deterministic classes:
- The JSON Schema rejects shape errors as `schema`.
- Cross-field semantics in `internal/contracts` (`manifestSemantics`,
  `inferenceEventSemantics`) reject rule violations as `context`.
- `Registry.ValidateInferenceEventTopic` binds an event to its topic and the asset
  registry, and rejects mismatches as `namespace`.

`tools/inference_contracts.py` is an independent Python oracle over the same shared
fixtures.

### Model manifest

- **Identity:** `manifest_id` (UUIDv7), `model_id`, semantic `model_version`,
  `created_at_ms`, and `runtime_type` (`statistical_v1`, `onnx_v1`, or
  `external_runner_v1`).
- **Provenance:** `artifact_sha256` (64 lowercase hex characters) and a `signature`.
- **Inputs:** 1–64 unique `(asset, measurement, unit)` bindings, all within one
  enterprise; `window_size_samples`; `window_duration_ms`; `required_quality: "good"`
  (only good samples feed a model); and `min_good_ratio`, the fraction of good samples
  a window needs before inference runs.
- **Outputs:** 1–16 bindings.
  - A `telemetry` soft sensor declares a measurement and unit. It must not overwrite a
    measurement the model reads, and no two outputs may write the same series. The
    `telemetry` kind name means "a numeric series", not the device topic: `hades-infer`
    publishes these values on `…/<asset>/derived/<measurement>`, never on device
    `telemetry` (Hades-036). The no-shadow rule is kept so a derived series name never
    reuses an input's name.
  - An `inference-event` output declares an `inference_type` and severity scores, with
    `warning_score ≤ critical_score`.
  - Telemetry outputs cannot carry severity, and event outputs cannot carry a unit.
- **Budgets:**
  - `max_memory_mb` (1–250, bounded by the Tier 2 IPC budget)
  - `max_inference_latency_ms`
  - `max_consecutive_failures`
  - `rollback_to_version`: `null` only for a model's first version; otherwise a strictly
    earlier semantic version than `model_version`.

### Inference event

The event carries `event_id` (UUIDv7), `timestamp`, `asset`, `inference_type`,
`model_id`, `model_version`, `severity` (`info`, `warning` or `critical`),
`window_start_ms`, `window_end_ms` and 1–16 `top_contributors`. These are all normalized
to 0–1:
- `anomaly_score`
- `threshold`
- `confidence`
- each contributor's `contribution`

The semantic rules:
- the window must be non-empty;
- the event `timestamp` must not precede `window_end_ms`, because an event is emitted
  when its window closes;
- `severity` must agree with the score: `warning` and `critical` require
  `anomaly_score ≥ threshold`, and `info` requires `anomaly_score < threshold`;
- contributors are unique.

## Signature profile

The manifest signature reuses the Hades-032 command signature shape: Ed25519 over the
RFC 8785 (JCS) canonical form with the `signature` member removed, an 88-character
base64 `value`, and a `key_id`. It adds `signer_id` (the release authority) beside
`key_id`, so `hades-infer` can apply the same trusted-keyring verifier that
`hades-actuator` uses. The signature covers `artifact_sha256`, so verifying the manifest
authenticates the artifact bytes. The schema checks only the shape. Verification needs
a keyring and the artifact, and belongs to Hades-036.

## MQTT and ACL separation

`hades-infer` is a separate certificate identity (`config/broker/security/roles.json`,
`deploy/mosquitto/acl.conf`). It may:

- subscribe to and receive the approved asset's `telemetry`;
- publish `event/+` for that asset only.

It has **no** access to `command/+`, `command_receipt/+`, metadata, status, or publishing
raw telemetry. No other identity may publish `event/+`. `consumer-primary` explicitly
opts in to receive `event/+`; `consumer-secondary` does not.

The anonymous development profile denies `event/#`. A static Mosquitto `deny` blocks
both publication and delivery, so anonymous clients can neither forge nor read inference
events. Develop against the secure profile, where `hades-infer` and `consumer-primary`
hold certificate identities.

`scripts/smoke-security` proves this against a real broker:
- the PUBACK and SUBACK denials;
- end-to-end delivery from `hades-infer` to `consumer-primary`;
- the denial for `consumer-secondary`.

Cloud export of inference events through `hades-transfer` is not granted here. The
transfer exporter handles telemetry, metadata and status only; adding events to it is
part of the Zeus ingest work (Zeus-001), together with a scoped transfer grant.

**Deviation from the ticket.** The ticket suggested `hades-infer` publish to "`/raw` or
`/event/+`". No MQTT `/raw` topic exists. Observations enter through authenticated HTTP
ingress and the normalizer before becoming `telemetry`, and granting a new raw MQTT
topic would bypass normalization. Hades-036 publishes soft-sensor values on their own
`derived/<measurement>` subtree instead. They never enter device `telemetry`, where
actuation preconditions read physical measurements. See
[edge inference](../operations/edge-inference.md).

Event topics are nine segments deep. The existing one-level consumer subscriptions
(`<asset>/+`) do not match them, so consumers must opt in to inference events explicitly.

## Compatibility and verification

The change is additive. All 16 prior contract files keep their baseline hashes; the
baseline adds two schemas and three fixture files. The validator selects message kinds
from the schema IDs present:
- the telemetry profile is required;
- the control profile is all-or-nothing;
- each Edge AI schema is compiled when present, so a partial rollout works;
- unknown IDs fail closed. Site manifests
still require only the seven telemetry/control schemas, so **existing signed site
bundles stay valid**. The Edge AI schemas join site bundles when Hades-036 deploys
`hades-infer`, through a reviewed regeneration. A test proves a seven-schema bundle
still compiles and rejects Edge AI kinds.

- `./scripts/check-contracts`: 38 inference fixtures. These cover unsigned or truncated
  signatures, missing or oversized budgets, invalid SHA-256 digests, unknown runtimes,
  malformed events, and semantic rejections.
- `go test -race ./internal/contracts/...`: fixture class checks, topic binding, and
  seven-schema compatibility.
- `python -m unittest discover -s tests`: the independent oracle.
- `scripts/smoke-security`: broker isolation for `hades-infer`.

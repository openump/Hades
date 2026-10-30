# Edge inference sidecar (`hades-infer`, Hades-036)

`hades-infer` runs one signed, budgeted Edge AI model next to the edge stack.

- It reads `telemetry` from the broker.
- It publishes `inference-event` messages on `…/<asset>/event/<inference_type>`.
- It publishes soft-sensor values on their own `…/<asset>/derived/<measurement>`
  subtree.
- It never writes device telemetry, never writes to a controller, and has no
  command-plane access.

The contracts are in [Edge AI inference](../contracts/edge-ai-inference.md).

## Model bundle

A bundle is a directory with two regular files (symlinks are refused):

| File | Content |
|---|---|
| `model-manifest.json` | A signed manifest (`urn:openump:schema:1.0:model-manifest`). |
| `model.artifact` | The model. For `statistical_v1`, a JSON detector configuration. |

Nothing activates until all three checks pass, in this order:

1. The manifest is schema-valid and passes the Hades-035 semantics.
2. The Ed25519 signature verifies against the operator keyring. The signature covers
   `"OpenUMP model-manifest v1\n"` followed by the RFC 8785 canonical manifest without
   `signature.value`. The domain prefix means a command or receipt signature can never
   verify as a manifest.
3. The SHA-256 of `model.artifact` equals `artifact_sha256`.

The keyring (`-keys`) is a JSON object mapping `key_id` to a base64 Ed25519 public key,
the same format `hades-actuator` uses. Under `hades-service`, pin the bundle files and the
keyring in the supervisor `files` map, so tampering also fails supervisor integrity
checks.

### `statistical_v1` artifact

```json
{
  "detectors": [
    {"kind": "zscore", "measurement": "motor_temperature", "scale": 3},
    {"kind": "ewma_drift", "measurement": "motor_temperature", "alpha": 0.2, "scale": 1},
    {"kind": "rate_of_change", "measurement": "hydraulic_pressure", "scale": 5},
    {"kind": "band_energy", "measurement": "vibration", "band_low": 0.5, "band_high": 1, "scale": 0.2}
  ],
  "soft_sensors": [
    {"kind": "band_energy_ratio", "measurement": "vibration", "output": "estimated_bearing_wear",
     "band_low": 0.5, "band_high": 1, "scale": 100}
  ]
}
```

Each detector maps a raw statistic to a 0–1 score using `1 − exp(−raw/scale)`, and the
model score is the highest detector score. `band_energy` uses a pure-Go radix-2 FFT. Bands
are fractions of the Nyquist frequency. Unknown fields and out-of-range parameters are
rejected.

`onnx_v1` and `external_runner_v1` models run in a separate process. `hades-infer` talks
to it over a local Unix socket (`-runner-socket`) with one newline-delimited JSON request
and one response per window. Both directions are capped at 1 MiB, and every call has the
latency deadline.

## Windows, budgets and rollback

- **Windows:** each bound measurement has a fixed ring of `window_size_samples`, allocated
  once. Samples older than or equal to the newest are dropped. A sample feeds the model
  only if its quality is `good`, its value is numeric and its unit matches the binding.
  A payload's `asset_id` must match the topic it arrived on, so one asset's publisher
  cannot feed another asset's model. Inference runs only when every window is full,
  spans at most `window_duration_ms`, and meets `min_good_ratio`.
- **No replay:** a window is consumed only by a successful run. A stalled source does not
  republish the same window with a new timestamp. A failed run publishes nothing and
  retries the same window, which is how the failure budget accumulates.
- **Latency budget:** each window runs under `max_inference_latency_ms`. A runner that
  ignores the deadline is abandoned when the deadline passes. Its goroutine ends when the
  runner returns, and at most `max_consecutive_failures` such goroutines can be
  outstanding before rollback stops all calls.
- **Rollback:** after `max_consecutive_failures` consecutive failures or timeouts, the
  engine switches atomically to the `-rollback-bundle` model. That bundle must be the
  manifest's declared `rollback_to_version`; otherwise the engine enters standby (no
  inference). The engine prints a `degraded` `producer_status` naming the cause
  (`latency-budget` or `failure-budget`).
- **Telemetry is unaffected:** `hades-infer` only subscribes to telemetry, so a failing
  model never blocks or drops it.

## Soft sensors on the derived subtree

Soft-sensor values are published on `openump/v1/…/<asset>/derived/<measurement>`, never
on the asset's device `telemetry` topic. This is deliberate:

- **They can't be taken for device readings.** `hades-actuator` reads `telemetry` for
  safety preconditions, and history, transfer and the catalog validate it as device
  telemetry from the asset's acquisition gateway. Keeping derived values out of that
  series means no model output can stand in for a physical sensor or feed an actuation
  gate.
- **The ingest path is unchanged.** The normalizer is not loosened.

Each derived value names its producing model (`model_id`, `model_version`) and the
window it summarizes (`window_start_ms`, `window_end_ms`). `observed_at` is the window end,
not the publish time, so a value always describes the data it was computed from.

`hades-infer` publishes `derived/+` only for its asset. `consumer-primary` opts in to
receive it. The development profile denies `derived/#` to anonymous clients.

`Registry.ValidateDerivedTopic` binds a derived topic to its asset. The payload is a
Hades-036 wire format validated by the producer. A formal versioned contract would join
the Hades-035 profile when a consumer such as history needs to ingest derived values.

## Memory

Measured under real load with a supervised `hades-infer` on a real mTLS broker (macOS
arm64): **22.1 MB resident**.

Hades-024/025 measured the baseline Linux reference stack, with each service under its own
supervisor process:
- one asset's pipeline: about 184–202 MB peak;
- broker and historian: about 36 MB.

Adding `hades-infer` puts a one-asset stack at **about 242–260 MB, at or slightly over the
250 MB Tier 2 budget**. This is not a demonstrated fit. Each per-service supervisor costs
about 17–20 MB, which makes supervision the main lever. Running services under a shared
supervisor, or trimming per-service overhead, should be measured on the target IPC before
claiming the budget.

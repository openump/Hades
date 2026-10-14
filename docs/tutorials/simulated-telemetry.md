# Simulated telemetry and validating consumer

This example uses Go and MQTT 5 against the [local development broker](mqtt-development.md). It is synthetic data, not an OT driver or production runtime. Start the broker first, then run the consumer in one terminal:

```sh
go run ./cmd/hades-consumer -count 3
```

Wait for its JSON `ready` message, then run in another terminal:

```sh
go run ./cmd/hades-simulator -scenario cycle -count 3
```

Both tools default to `mqtt://127.0.0.1:1883`. For another local port, pass `-broker mqtt://127.0.0.1:18883` to both. The examples reject non-loopback endpoints, credentials in URLs, and non-MQTT schemes. Use `-timeout` to change the total run deadline. Counts are bounded to 1–10,000.

## Scenarios and context

`normal` emits deterministic good-quality temperatures. `bad-quality` preserves a numeric diagnostic value with bad quality. `missing-reading` emits null with bad quality and a timeout reason. `cycle` repeats those three scenarios in order. Observation time follows the actual host clock; synthetic value selection follows the sequence number.

[The sample metadata snapshot](../../examples/site/metadata.json) defines asset identity, hierarchy, source, units, numeric range, and acquisition/status intervals. The simulator publishes that exact snapshot retained, including after reconnect. The consumer preloads the approved snapshot so retained-message ordering cannot cause it to invent context. Received metadata must match the approved snapshot, and telemetry must match its exact revision, gateway, protocol, type, and unit.

To change the rate or units, copy the snapshot to another file, edit it, and pass the same `-config /absolute/path/to/metadata.json` to both tools. Supported synthetic temperature units are `Cel` and `K`; Kelvin conversion is performed by the generator. When changing units, update the valid range too (for example -40–150 Cel corresponds to 233.15–423.15 K). The measurement's `expected_interval_ms` and acquisition's `expected_status_interval_ms` must agree in this example, and must not exceed eight hours. The simulator uses that configured interval between observations.

Changing context requires a higher metadata revision, a new metadata message UUID, and an appropriate publication timestamp. Keep the resulting snapshot immutable when rerunning it. The shipped file is a development snapshot, not a live registry/archive or proof of engineering provenance.

Metadata and status use retention; telemetry does not. Status includes a fresh connection-session UUID, boot UUID, initial/reconnect heartbeat, per-observation acquisition state, graceful shutdown, and an offline last will. Status expiry follows the configured interval. Status output describes producer/acquisition fields; this example does not implement authoritative cross-session availability reconciliation or machine operating-state inference.

## Validation and retry

The consumer emits JSON lines containing the asset, observation time, unit, value, quality, freshness, message ID, and validated envelope. Bad quality and stale/unknown freshness remain distinct. The sample uses zero relative clock uncertainty because both tools run on the same host; that is not a sensor-clock accuracy claim.

Each observation is generated once before publication attempts. Retries copy the same bytes, ID, observation time, and sequence. The sample has at most 20 application attempts with bounded delays and a total deadline; it is not the production delivery worker from Hades-007. The SDK's QoS 1 reconnect delivery may also produce duplicates. The consumer maintains a bounded 4,096-entry identity/digest cache, reports duplicates, rejects conflicting content, and counts only unique telemetry toward its exit count. Cache eviction and process restart mean it is not a durable exactly-once consumer.

The inbound consumer channel holds at most 64 observations; overflow fails the example rather than accumulating indefinitely. Payloads and configuration reads are bounded. Neither tool provides a durable disk queue, OT connectivity, historian ingestion, or cloud acceptance.

## Repeatable verification

```sh
go test -race ./...
./scripts/check-contracts
./scripts/smoke-simulator
```

The smoke script requires Docker Compose, Go, and Python 3 for test assertions only. It uses the existing development stack, intentionally restarts its broker, and leaves the stack/volume in place. It verifies six unique observations, all three scenarios, duplicate handling, automatic client resubscription, and unchanged IDs/timestamps against producer records. It revalidates every captured envelope using the Go conformance CLI.

The Linux CI broker job runs the same script in an isolated project and cleans up only its disposable volume. Production application binaries do not require Python. When changing the default host port, export `HADES_MQTT_PORT` for the smoke script.

## Dependency source

The examples use [Eclipse Paho Go MQTT 5](https://pkg.go.dev/github.com/eclipse/paho.golang@v0.23.0) with its connection manager. Exact runtime versions and hashes are pinned in go.mod/go.sum. TLS and production authentication remain Hades-013 work.

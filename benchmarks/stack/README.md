# Multi-context reference workloads (Hades-025)

Run from the repository with Go, Python dependencies from requirements-dev.txt,
and a working Docker daemon:

```sh
./scripts/benchmark-stack --assets 2 --count 8 --host-label my-disposable-host --output artifacts/stack-capacity/two-assets
./scripts/benchmark-stack --assets 4 --count 8 --rate 8 --burst-rate 24 --envelope-bytes 4096 --host-label my-disposable-host --output artifacts/stack-capacity/four-assets
./scripts/benchmark-stack --assets 2 --count 4 --no-transfer --host-label my-disposable-host --output artifacts/stack-capacity/disabled
```

On disposable Linux add `--systemd` to install/start/probe/stop temporary systemd
units using sudo as the current non-root account. No system services with existing
names are changed. Broker/history are fresh isolated Compose volumes with random
project names. Host adapters use private temporary state. Cleanup stops these
processes and removes only these units/volumes. Output must be a new directory.
Do not run the development anonymous loopback profile on a production host.
The role-isolated mTLS data path is separately exercised by `smoke-security` and
`smoke-ot-gateway`; this capacity profile omits the history TLS proxy.

Native reference mode needs exact pinned Mosquitto 2.1.2, its matching
mosquitto_sub and VictoriaMetrics 1.154.0:

```sh
export HADES_MOSQUITTO_BINARY=/absolute/path/mosquitto
export HADES_MQTT_SUB=/absolute/path/mosquitto_sub
export HADES_VM_BINARY=/absolute/path/victoria-metrics-prod
export HADES_PYTHON=/absolute/path/venv/bin/python
```

The harness creates one approved metadata context and normalizer/history/transfer
instance per asset. Both Go and independent C/Python consumers validate each
context; schema/topic validation remains enabled. This measures the reference
per-context deployment cost, not a multiplexed future runtime. Asset count also
controls numeric label-set cardinality. Currently each context has exactly one
motor-temperature measurement. Arbitrary measurement names/counts are **not yet
supported**. Envelope size is valid JSON whitespace padding at raw ingress; the
normalized MQTT envelope remains compact. Do not interpret raw padding as a test
of large multi-measurement MQTT envelopes.

Half of `--count` observations per asset run at `--rate` total observations/sec,
the remainder at `--burst-rate` while the fixture receiver returns 503. Duration
is derived from count/rate; it is not an independent input. The driver is serial,
so slower HTTP admission lowers achieved throughput and increases schedule lag.
Local history and both consumers must finish during the cloud outage. Transfer
is stopped, upgraded, rolled back and retried without dropping owned IDs. Exact
normalized payload bytes and source IDs/timestamps are compared against local
and durable remote records. Numeric historian timestamps/values are also queried.
Malformed JSON and an unapproved context must fail at the actual ingress.

Bounds: 2–8 assets, 4–1000 observations per asset, steady rate ≤100/s, burst ≤200/s,
512–60000 raw bytes, 60–900 seconds, and 64–2048 MiB private run logical file
budget (default 512 MiB). Scheduling must fit half the wall-clock budget. Sampling
reports a bound violation; a current HTTP/readiness operation may finish before
cleanup. Startup/cleanup and Docker image builds can exceed the workload window.
Queues retain their own record/byte/reserve limits. The synthetic driver is not
an acquisition service and has no hardware connections.

Reports include binary digests, source revision/dirty status, host inventory,
raw source and resource samples, admission latency, schedule lag, per-process CPU
and RSS, logical file growth and ID comparisons. First visibility latency is a
periodic file/consumer scan **upper bound** with missing counts and overhead,
not an in-process latency histogram. CPU deltas omit a process's unsampled final
interval; short-lived consumers may lack samples. RSS includes shared pages.
Docker host PID sampling works on Linux, not the hidden Docker VM on macOS.
Docker historian file growth is currently unknown; native mode measures its
logical files. Sparse/allocation/write amplification and media endurance remain
unknown. Version labels in native reports are expected pins: verify installed
binaries yourself; binary digests are retained.

Use the sizing worksheet's actual bytes/record, pending occupancy, replay rate
and reserve/headroom as inputs to [retention sizing](../../docs/operations/retention-sizing.md).
Short-run startup/segment overhead makes extrapolated retention uncertain. These
runs establish functional reference coverage only. Hades-022 target-host capacity,
OPC UA acquisition and Hades-023 operator sign-off remain separate gates.

Outstanding Hades-025 scope: configurable multi-measurement contexts and payload
cardinality, explicit burst duration, internal processing latency distributions,
container historian disk accounting, and a fuller measured retention projection.
The report deliberately lists these missing measurements. The existing
`hades-capacity` intake-only microbenchmark keeps its separate scope.

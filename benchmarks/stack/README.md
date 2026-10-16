# Multi-context and OPC UA pilot capacity workloads (Hades-022 / Hades-025)

Run from the repository with Go, Python dependencies from requirements-dev.txt,
and a working Docker daemon:

```sh
./scripts/benchmark-stack --opcua-pilot --count 8 --host-label my-disposable-host --output artifacts/stack-capacity/opcua-pilot
./scripts/benchmark-stack --assets 2 --measurements 2 --count 8 --burst-seconds 1 --host-label my-disposable-host --output artifacts/stack-capacity/two-assets
./scripts/benchmark-stack --assets 4 --measurements 4 --count 8 --burst-seconds 1 --rate 8 --burst-rate 24 --envelope-bytes 4096 --host-label my-disposable-host --output artifacts/stack-capacity/four-assets
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
per-context deployment cost, not a multiplexed future runtime. Asset and measurement
counts control numeric label-set cardinality (`assets * measurements`). The opt-in
`approved-numeric` normalizer profile accepts simulator provenance and 1–64 reviewed
numeric definitions; raw names/units must match the metadata. It runs the pure
Bento identity mapping on canonical numeric signals, validates the resulting
telemetry/context/topic, and preserves numeric precision, IDs, timestamps and
provenance. The existing temperature/deci-Celsius and Modbus behavior remains the
default. The workload bounds measurement count to 1–32 per asset. Go and Python
consumers validate and summarize all measurements.

`--envelope-bytes` sets a minimum raw envelope size using valid JSON whitespace;
adding measurements also increases the actual normalized MQTT envelope. Reports
include actual raw bytes, measurement count and label-set count. No unsupported
padding fields are smuggled into the shared telemetry contract.

Half of `--count` observations per asset run at `--rate` total observations/sec,
the remainder at `--burst-rate` while the fixture receiver returns 503. `--burst-seconds` independently overrides the burst length, rounding up to whole
per-asset observation cycles; total count and consumer expectations are adjusted.
Without it, duration is derived from count/rate. The driver is serial,
so slower HTTP admission lowers achieved throughput and increases schedule lag.
Local history and both consumers must finish during the cloud outage. Transfer
is stopped, upgraded, rolled back and retried without dropping owned IDs. Exact
normalized payload bytes and source IDs/timestamps are compared against local
and durable remote records. Numeric historian timestamps/values are also queried.
Malformed JSON and an unapproved context must fail at the actual ingress.

Bounds: `--opcua-pilot` fixes 1 asset (`press-04`), 3 reviewed signals (`motor_temperature`, `cycle_counter`, `machine_state` at 1 Hz polls = 3 observations/s), and 4–200 poll cycles (`12..600` observations) through the pinned read-only `hades-opcua-gateway` and `hades-opcua-mock` (`Aes256_Sha256_RsaPss`, `SignAndEncrypt`) with source ledger cross-check. Synthetic multi-context mode bounds 2–8 assets, 1–32 measurements per asset, 4–1000 observations per asset, steady rate ≤100/s, burst ≤200/s,
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
Docker historian apparent bytes are measured with container `du -sb`; native mode
sums logical file lengths. Directory overhead differs between these methods. Sparse/allocation/write amplification and media endurance remain
unknown. Native component version output must match the expected pins, and binary digests
and normalizer Go build/dependency information are retained.

Use the sizing worksheet's actual bytes/record, pending occupancy, replay rate,
reserve/headroom, and `tier2_reference_ipc_model` (VictoriaMetrics 30d/90d retention,
transfer 7d outage capacity vs v1 `100,000`-record / `256 MiB` caps, history 7d
`api_accepted` dedup store window, and SSD endurance write-amplification bounds)
as inputs to [retention sizing](../../docs/operations/retention-sizing.md) and
[capacity report](../../docs/operations/capacity-report.md).
Short-run startup/segment overhead makes extrapolated retention uncertain.
Physical Tier 2 IPC validation and Hades-023 operator sign-off remain separate gates.

The existing `hades-capacity` intake-only microbenchmark keeps its separate scope.
Processing distributions use fixed disjoint millisecond buckets in normalizer
health and historian write stats, with an unbounded >1000 ms final bucket. These
are process-lifetime counters (including the two negative setup probes on the
first normalizer). Admission/visibility distributions use the measurement window;
visibility includes sampling delay. Transfer worker restarts reset its counters;
source/local/remote ledgers, not process counter sums, establish delivery totals.

Installed Linux runs also exercise a CPU-bound fixture under the 50% CPU quota,
force a disposable memory fixture through the 512 MiB cgroup limit and automatic
restart, and assert actual private log rotation stays within two 64 KiB files.
Resource fixtures finish before the workload measurement window. Their journals
and limits are recorded as lifecycle evidence. These fixture failures are
intentional and do not involve production services.

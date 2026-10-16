# Capacity and retention measurements — software pilot and multi-context workloads (Hades-022)

Hades-022 measures CPU, RAM, disk growth, throughput, and processing latency across
documented asset counts, rates, payloads, and label cardinality, and calculates
retention, transfer outage capacity, and storage endurance bounds for the active
OPC UA software pilot (`press-04`) and multi-context workloads against the
**Tier 2 Standard Line IPC** reference profile (`4-core linux/amd64`, `4–8 GB RAM`,
`256 GB Industrial M.2 NVMe / SATA SSD with PLP`).

All measurements in this report are recorded on disposable hosts (`local-macos-arm64-disposable`
and `github-ubuntu-24.04` Linux CI) with `"pilot_qualified": false`. Neither a
developer workstation nor a CI runner is a physical Tier 2 plant IPC; physical
hardware qualification and `Hades-023` operator sign-off remain separate gates.
No unsupported percentage savings or unlimited-retention claims are made.

## Repeatable workloads

### 1. Whole-stack OPC UA software pilot and multi-context harness (`scripts/benchmark-stack`)

```sh
./scripts/benchmark-stack --opcua-pilot --count 8 --host-label <machine-inventory-id> --output artifacts/stack-capacity/opcua-pilot
./scripts/benchmark-stack --assets 2 --measurements 2 --count 10 --burst-seconds 2 --rate 4 --burst-rate 12 --envelope-bytes 1024 --host-label <machine-inventory-id> --output artifacts/stack-capacity/two-assets
./scripts/benchmark-stack --assets 4 --measurements 4 --count 10 --burst-seconds 2 --rate 8 --burst-rate 16 --envelope-bytes 2048 --host-label <machine-inventory-id> --output artifacts/stack-capacity/four-assets
```

On disposable Linux hosts (including CI), add `--systemd` to exercise installed
`systemd` unit lifecycle, 50% CPU quota enforcement, 512 MiB memory limit OOM
recovery, and bounded 2×64 KiB log rotation before the measurement window.

### 2. Serial transfer-intake microbenchmark (`cmd/hades-capacity`)

```sh
go build -o artifacts/hades-capacity ./cmd/hades-capacity
artifacts/hades-capacity -host-label <machine-inventory-id> -count 200 -rate 50 > steady.json
artifacts/hades-capacity -host-label <machine-inventory-id> -count 200 -rate 0 > burst.json
```

The intake-only Go tool creates/removes a private temporary queue without PLC,
broker, historian, or cloud traffic. It measures isolated `internal/transfer`
synced intake latency, CPU, RSS, and per-record storage accounting.

## Whole-stack measurements (`2026-10-16`)

Measured across three whole-stack profiles on `local-macos-arm64-disposable`
(Apple M3 Pro, 12 logical CPUs, 36 GiB RAM, Go `1.27.2`, pinned Mosquitto `2.1.2`,
pinned VictoriaMetrics `1.154.0`) and verified on disposable Linux (`github-ubuntu-24.04`
with `--systemd` in the `service-capacity` CI workflow, artifact `stack-capacity-reference`):

- **`opcua-pilot`**: 1 asset (`press-04`, `examples/site/opcua-mock.metadata.json`),
  3 reviewed signals (`motor_temperature` °C, `cycle_counter` count, `machine_state` state)
  polled at 1 Hz (`3.0` observations/s) over OPC UA `Aes256_Sha256_RsaPss` `SignAndEncrypt`
  (`github.com/awcullen/opcua v1.4.0`) through `hades-opcua-gateway` -> mTLS HTTP ->
  `hades-normalizer` -> MQTT -> `hades-history` (VictoriaMetrics), `hades-transfer`
  (durable receipt receiver with outage/upgrade/rollback/replay), Go consumer, and C/Python consumer.
- **`two-assets`**: 2 assets × 2 numeric measurements (`4` numeric label sets),
  `1,024 B` minimum raw envelope, `4.0/s` steady rate, `12.0/s` burst during cloud outage.
- **`four-assets`**: 4 assets × 4 numeric measurements (`16` numeric label sets),
  `2,048 B` minimum raw envelope (`4,096 B` in Linux CI), `8.0/s` steady rate, `16.0/s` (`24.0/s` in CI) burst during cloud outage.

| Metric | `opcua-pilot` (1 asset, 3 signals) | `two-assets` (2 assets, 4 series) | `four-assets` (4 assets, 16 series) |
| --- | --- | --- | --- |
| Scope | `opcua_software_pilot_stack` | `disposable_synthetic_multi_context_stack` | `disposable_synthetic_multi_context_stack` |
| Assets / measurements per asset / numeric label sets | `1 / 3 / 3` | `2 / 2 / 4` | `4 / 4 / 16` |
| Admitted observations (`refused`) | `24 (0)` | `34 (0)` | `52 (0)` |
| Scheduled steady / burst rate (obs/s) | `3.0 / 3.0` | `4.0 / 12.0` | `8.0 / 16.0` |
| Achieved overall admission rate (obs/s) | `3.42` | `7.84` | `11.85` |
| HTTP mTLS admission latency p50 / p95 / p99 (ms) | `0.52 / 2.03 / 2.28` | `1.49 / 5.21 / 5.90` | `1.55 / 2.97 / 4.84` |
| Schedule lag p50 / p95 / p99 (ms) | `11.00 / 21.00 / 22.00` | `3.91 / 7.38 / 11.08` | `4.30 / 5.59 / 5.64` |
| Sampled first visibility — local history p50 / p95 / p99 (ms, upper bound) | `273.0 / 623.4 / 626.4` | `373.2 / 729.3 / 786.8` | `506.0 / 964.9 / 1256.7` |
| Sampled first visibility — Go consumer p50 / p95 / p99 (ms, upper bound) | `273.6 / 623.8 / 626.8` | `301.9 / 632.4 / 729.7` | `460.6 / 913.0 / 1010.8` |
| Sampled first visibility — C/Python consumer p50 / p95 / p99 (ms, upper bound) | `274.0 / 624.0 / 627.0` | `302.1 / 632.7 / 730.0` | `461.3 / 913.2 / 1013.1` |
| Sampled first visibility — remote receipt p50 / p95 / p99 (ms, includes outage) | `639.4 / 4413.1 / 4414.1` | `2112.2 / 3499.1 / 3665.1` | `2307.9 / 4633.7 / 5130.5` |
| Sum of mean core equivalents across all sampled processes | `0.0479 cores` | `0.0795 cores` | `0.1025 cores` |
| Sum of sampled peak RSS across all processes (MiB, overcounts shared pages) | `343.91 MiB` | `442.41 MiB` | `838.50 MiB` |
| Logical queue bytes per pending record at outage | `1,457.8 B` | `1,053.9 B` | `1,217.8 B` |
| Accounted queue bytes per pending record (includes 4,096 B overhead) | `5,553.8 B` | `5,149.9 B` | `5,313.8 B` |
| History adapter store growth per observation (bytes) | `2,041.7 B` | `2,121.5 B` | `3,274.7 B` |
| VictoriaMetrics logical storage growth (total / per observation) | `1,932 B / 80.50 B` | `1,296 B / 38.12 B` | `3,600 B / 69.23 B` |
| Outage backlog records / replay duration (s) / replay throughput (rec/s) | `12 / 0.223 s / 53.79/s` | `24 / 0.413 s / 58.10/s` | `32 / 2.203 s / 14.52/s` |

### Per-component CPU and RSS breakdown (`opcua-pilot`)

| Component | Mean core equivalents | Sampled peak RSS (MiB) | Notes |
| --- | --- | --- | --- |
| `opcua-mock` | `0.0020` | `34.80` | Pinned OPC UA mock server (`press-04`) |
| `press-04-gateway` (+ supervisor) | `0.0000` | `20.70` (+ `17.33`) | Read-only OPC UA gateway (`Aes256_Sha256_RsaPss`) |
| `press-04-normalizer` (+ supervisor) | `0.0020` | `26.22` (+ `17.47`) | Bento normalizer (`23/26` admissions `<0.5 ms`) |
| `press-04-history` (+ supervisor) | `0.0132` | `20.42` (+ `15.94`) | History adapter (`14` VictoriaMetrics batches) |
| `press-04-transfer` (+ supervisor) | `0.0111` | `20.77` (+ `17.45`) | Transfer adapter across outage, upgrade, rollback |
| `broker` (Mosquitto 2.1.2) | `0.0010` | `11.28` | Local MQTT broker |
| `historian` (VictoriaMetrics 1.154.0) | `0.0061` | `58.53` | Local time-series historian (`-memory.allowedBytes=128MiB`) |
| `press-04-primary` / `press-04-secondary` / `press-04-c-transport` | `0.0040` | `20.12 / 36.30 / 3.91` | Independent Go and C/Python validating consumers |
| `press-04-receiver` | `0.0040` | `20.31` | Durable cloud receipt fixture |

## Tier 2 Reference IPC sizing, retention, outage, and endurance evaluation

Evaluated against the **Tier 2 Standard Line IPC** reference profile (`4-core linux/amd64`,
`4–8 GB RAM`, `256 GB Industrial M.2 NVMe / SATA SSD with PLP`, target `30–90 days`
local historian retention, target `7-day` cloud-outage buffer):

1. **CPU and RAM headroom on Tier 2 IPC (`4 cores`, `4–8 GB RAM`)**:
   - Measured whole-stack CPU is `0.048 cores` (`1.2%` of 4 cores) for the 1-asset OPC UA
     pilot (`3 obs/s`) and `0.103 cores` (`2.6%` of 4 cores) for 4 assets (`8–16 obs/s`).
   - Sum of per-process peak RSS is `343.9 MiB` (1 asset) to `838.5 MiB` (4 assets,
     with per-asset process duplication), well within `4–8 GB RAM` and the `512 MiB`
     per-service `systemd` `MemoryMax` ceiling.
2. **Local historian retention (`VictoriaMetrics`, target `30–90 days`)**:
   - At measured `opcua-pilot` growth (`80.5 B/observation` including short-run index/part
     creation overhead at `3.0 obs/s` = `259,200 obs/day`), illustrative daily historian
     growth is `20.87 MB/day` (`~19.90 MiB/day`), or `~626.0 MB` (`~597.0 MiB`) for
     `30 days` and `~1.88 GB` (`~1.75 GiB`) for `90 days` (at 4 assets / `8.0 obs/s`:
     `47.85 MB/day`, `~1.44 GB` for `30 days`, `~4.31 GB` for `90 days`).
   - **Operational configuration constraints**:
     - `deploy/compose.yaml` defaults to `-retentionPeriod=7d`; operators targeting
       30–90 day retention must explicitly set `-retentionPeriod=30d` or `-retentionPeriod=90d`.
     - `internal/history/history.go` (`WindowCheck`) rejects observations older than
       `24 hours`, bounding offline historian backfill to `24 hours`.
     - `internal/history/history.go` retains `api_accepted` records for `7 days` in its
       local dedup store (`100,000` records / `256 MiB` accounted cap). At `3.0 obs/s`
       (`10,800 obs/hr`), the v1 flat-file history store reaches its `256 MiB` accounted
       cap (`~48,333` records at `5,553.8 B/record`) in **`~4.48 hours`** (`~7.02 hours`
       per asset at `2.0 obs/s/asset` in `four-assets`), after which `hades-history`
       refuses new intake until 7-day-old `api_accepted` records expire or segmented WAL
       compaction (`Hades-026`) removes the `4,096 B` per-record flat-file charge and
       in-memory payload retention.
3. **Transfer outage capacity (`hades-transfer`, target `7-day` cloud outage)**:
   - Under the v1 per-asset store limits (`100,000` records, `256 MiB` accounted with
     `4,096 B` per-record overhead), measured `opcua-pilot` pending records (`1,457.8 B`
     logical, `5,553.8 B` accounted) bind on the `256 MiB` accounted limit at
     **`48,333` records**, which provides **`4.48 hours` (`16,111 s`)** of cloud-outage
     buffering at `3.0 obs/s` (`52,124` records / `7.24 hours` per asset at `2.0 obs/s/asset`).
   - A **7-day (`168-hour`) cloud outage** at `3.0 obs/s` produces **`1,814,400` records**
     requiring **`2.65 GB` (`2.46 GiB`) logical storage** (`10.08 GB` / `9.38 GiB` under
     v1 `4,096 B`-overhead accounting), and furthermore hits the **72-hour replay window**
     in `internal/transfer/queue.go` (`q.now().Sub(r.Accepted) > 72*time.Hour`), which
     suspends records older than 72 hours (`replay_window_requires_reconciliation`).
   - Achieving a 7-day unattended outage therefore requires both segmented WAL storage
     (`Hades-026`) with raised record/byte budgets (`>= 2,000,000` records, `>= 4 GiB`
     per asset) and operator reconciliation for records exceeding the 72-hour replay window.
4. **SSD endurance and physical write amplification (`150 TBW` Tier 2 industrial SSD)**:
   - In the v1 flat-file store (`internal/storage/store.go`), each observation performs
     **4 synced file mutations (`7 fsync` calls)** across `hades-history` (`pending` ->
     `api_accepted`) and `hades-transfer` (`pending` -> `Delete`), plus Mosquitto and
     VictoriaMetrics flushes.
   - Assuming `16 KiB–64 KiB` physical flash write per synced file mutation (filesystem
     journal + metadata + 4 KiB page/block amplification), `3.0 obs/s` (`259,200 obs/day`)
     generates **`16.99–67.95 GB/day`** of physical writes (**`6.20–24.80 TBW/year`**,
     or `4.1%–16.5%` of a `150 TBW` industrial SSD rating per year; at `8.0 obs/s`
     across 4 assets: `45.30–181.19 GB/day`, or **`16.53–66.14 TBW/year`**).
   - Actual physical write amplification must be verified on the target Linux IPC using
     NVMe/SATA SMART counters (`Data Units Written` / `Host_Writes_32MiB`) and `/proc/diskstats`
     over a multi-hour soak.

## Intake-only development reference (`2026-10-15`)

Committed JSON under `benchmarks/reference-{steady,burst}.json` records the earlier
isolated `hades-capacity` transfer-intake microbenchmark on `local-macos-arm64-disposable`
(1 asset, 1 numeric measurement, 200 observations, mean payload `483.26 B`):

| Measurement | Paced 50/s | Unpaced serial |
| --- | --- | --- |
| Observations | 200 | 200 |
| Duration (seconds) | 3.99 | 2.34 |
| Achieved records/second | 50.10 | 85.63 |
| Synced admission p95 / p99 (ms) | 14.89 / 20.08 | 14.69 / 21.08 |
| Process CPU seconds | 0.369 | 0.316 |
| Process maximum RSS (MiB) | 18.06 | 17.81 |
| Queue file growth / accounted bytes per record | 971.67 / 5,067.67 | 971.68 / 5,067.68 |
| Refused observations | 0 | 0 |

See [retention sizing](retention-sizing.md) for the sizing formulas and worked
Tier 2 IPC calculations, and [workload commands and bounds](../../benchmarks/stack/README.md)
for harness constraints.

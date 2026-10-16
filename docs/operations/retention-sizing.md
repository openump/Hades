# Retention and outage sizing worksheet

Use measurements from the intended host and workload. Never substitute queue
JSON size for historian compression or physical device writes.

## 1. Formulas

Let `R = assets × observations_per_second_per_asset` be the total envelope arrival
rate (where each observation carries `M` numeric or discrete measurements per
envelope, yielding `assets × M` label sets). Add metadata/status and retries separately.
Let `Bq` be measured **accounted queue bytes per record** (including the store's
4 KiB charge), `Bl` be measured **logical file bytes per record**, `Qb` the configured
byte budget, `Qr` the record budget, and `S` the already suspended/owned record count.
Empty-queue outage capacity is:

`records = min(Qr, floor(Qb / Bq))`

`outage_seconds = available_records / incoming_records_per_second`

Subtract current/suspended occupancy before estimating remaining capacity. The
72-hour replay window (`internal/transfer/queue.go`) suspends records older than
72 hours for reconciliation; it does not create space. Receiver recovery must
sustain more throughput than live acquisition:
`catchup_seconds = backlog / (replay_rate - incoming_rate)` only when that
difference is positive. File/metadata churn and retained receipts need budgets.

For historian retention, measure growth of the actual pinned historian over
representative cardinality and duration, including index/compaction overhead.
Use `daily_growth = measured_growth / measured_seconds × 86400` (or
`bytes_per_observation × R × 86400`) only with explicit uncertainty bounds; short
empty-database runs include partition/index creation overhead.
`retention_days <= historian_budget / daily_growth`, with explicit free-space,
compaction, backup staging and failure headroom. Confirm through longer runs.

Storage endurance requires the device model's rated endurance and measured
physical writes, including `fsync`, broker autosave, queue admission/retirement,
filesystem and database write amplification. Logical file growth is not TBW:

`daily_physical_writes = R × 86400 × synced_mutations_per_observation × physical_bytes_per_mutation`

`annual_tbw = daily_physical_writes × 365 / 10^12`

Record device age/health and an uncertainty range. Do not select universal
percentage headroom or claim unlimited retention from these formulas.

## 2. Worked Tier 2 Reference IPC calculations (`Hades-022`, `2026-10-16`)

Target reference hardware profile: **Tier 2 Standard Line IPC** (`4-core linux/amd64`,
`4–8 GB RAM`, `256 GB Industrial M.2 NVMe / SATA SSD with PLP`, `150 TBW` rated endurance,
`128 MiB` per-store disk reserve).

| Sizing parameter | `opcua-pilot` (`press-04`, 1 asset, 3 signals, `3.0 obs/s`) | `two-assets` (2 assets, 4 series, `4.0 obs/s` steady) | `four-assets` (4 assets, 16 series, `8.0 obs/s` steady) |
| --- | --- | --- | --- |
| Measured logical / accounted queue bytes per record (`Bl / Bq`) | `1,457.8 B / 5,553.8 B` | `1,053.9 B / 5,149.9 B` | `1,217.8 B / 5,313.8 B` |
| v1 per-asset store caps (`Qr / Qb`) | `100,000 records / 256 MiB` | `100,000 records / 256 MiB` | `100,000 records / 256 MiB` |
| Effective v1 records per asset (`min(Qr, floor(Qb / Bq))`) | `48,333 records` | `52,124 records` | `50,516 records` |
| **v1 max cloud-outage duration at steady rate** | **`4.48 hours` (`16,111 s`)** | **`7.24 hours` (`26,062 s`)** | **`7.02 hours` (`25,258 s`)** |
| **v1 history 7d `api_accepted` dedup store time-to-full** | **`4.48 hours`** | **`7.24 hours`** | **`7.02 hours`** |
| Measured replay rate (`replay_rate`) vs steady `R` | `53.79/s` vs `3.0/s` (`50.79/s` net catch-up) | `58.10/s` vs `4.0/s` (`54.10/s` net catch-up) | `14.52/s` vs `8.0/s` (`6.52/s` net catch-up) |
| Required records for **7-day (`168 h`) outage** | `1,814,400 records` | `2,419,200 records` (`1.21M/asset`) | `4,838,400 records` (`1.21M/asset`) |
| Required logical / v1-accounted bytes for **7-day outage** | `2.65 GB` (`2.46 GiB`) / `10.08 GB` (`9.38 GiB`) | `2.55 GB` (`2.37 GiB`) / `12.46 GB` (`11.60 GiB`) | `5.89 GB` (`5.49 GiB`) / `25.71 GB` (`23.94 GiB`) |
| Measured VictoriaMetrics growth per observation | `80.50 B/obs` | `38.12 B/obs` | `69.23 B/obs` |
| **Projected VictoriaMetrics 1d / 30d / 90d storage** | **`20.87 MB / 626.0 MB / 1.88 GB`** | **`13.17 MB / 395.2 MB / 1.19 GB`** | **`47.85 MB / 1.44 GB / 4.31 GB`** |
| **Projected physical SSD writes (`16–64 KiB` × 4 synced mutations/obs)** | **`16.99–67.95 GB/day` (`6.20–24.80 TBW/yr`)** | **`22.65–90.60 GB/day` (`8.27–33.07 TBW/yr`)** | **`45.30–181.19 GB/day` (`16.53–66.14 TBW/yr`)** |

## 3. Operating recommendations and explicit bounds

1. **Current v1 operating envelope (flat-file store)**:
   - Safe unattended cloud-outage and history-dedup operating window at `3.0 obs/s`
     (`press-04` OPC UA pilot) is **`<= 4.0 hours`** (`<= 6.5 hours` per asset at `2.0 obs/s/asset`).
   - Do not claim 7-day outage buffering or multi-day unattended operation on the
     v1 `100,000`-record / `256 MiB` flat-file store, because both `hades-transfer`
     (during an outage) and `hades-history` (even during healthy operation, due to
     its 7-day `api_accepted` dedup retention) reach `256 MiB` accounted capacity in `~4.48 hours`.
2. **Requirements for 30–90 day retention and 7-day outage tolerance on Tier 2 IPC**:
   - Adopt segmented WAL storage (`Hades-026`) so retired/delivered records and
     in-memory payloads do not incur `4,096 B` per-file overhead or unbounded RAM growth.
   - Increase per-asset transfer queue limits to `>= 2,000,000` records and `>= 4 GiB`
     for a 1-asset `3.0 obs/s` pilot, and define an operator reconciliation runbook
     for records older than the `72-hour` replay window (`internal/transfer/queue.go`)
     and the `24-hour` historian backfill cutoff (`internal/history/history.go`).
   - Configure VictoriaMetrics `-retentionPeriod=30d` (or `90d`) in `deploy/compose.yaml`
     instead of the `7d` development default; `256 GB` SSD capacity easily accommodates
     the projected `0.63–4.31 GB` 90-day historian footprint alongside OS, container images,
     and `128 MiB` per-store disk reserves.
3. **Endurance and physical host verification**:
   - Even under conservative flat-file write amplification (`16–64 KiB` per synced
     mutation, `6.20–24.80 TBW/yr` at `3.0 obs/s`), annual wear remains within a
     `150 TBW` industrial SSD rating, and segmented append-only WAL (`Hades-026`)
     reduces small-file metadata churn.
   - Before production pilot sign-off (`Hades-023`), capture NVMe/SATA SMART write
     counters and `/proc/diskstats` over a multi-hour run on the physical Tier 2 IPC.

# Retention and outage sizing worksheet

Use measurements from the intended host and workload. Never substitute queue
JSON size for historian compression or physical device writes.

Let `R = assets × measurements per asset × observations per second`, adjusted
for the actual envelope grouping. Add metadata/status and retries separately.
Let `Bq` be measured **accounted queue bytes per record** (including the store's
4 KiB charge), `Qb` the configured byte budget, `Qr` the record budget, and `S`
the already suspended/owned record count. Empty-queue outage capacity is:

`records = min(Qr, floor(Qb / Bq))`

`outage_seconds = available_records / incoming_records_per_second`

Subtract current/suspended occupancy before estimating remaining capacity. The
72-hour replay window suspends records for reconciliation; it does not create
space. Receiver recovery must sustain more throughput than live acquisition:
`catchup_seconds = backlog / (replay_rate - incoming_rate)` only when that
difference is positive. File/metadata churn and retained receipts need budgets.

For historian retention, measure growth of the actual pinned historian over
representative cardinality and duration, including index/compaction overhead.
Use `daily_growth = measured_growth / measured_seconds × 86400` only after a
steady-state interval; short empty-database runs can distort this estimate.
`retention_days <= historian_budget / daily_growth`, with explicit free-space,
compaction, backup staging and failure headroom. Confirm through longer runs.

Storage endurance requires the device model's rated endurance and measured
physical writes, including fsync, broker autosave, queue admission/retirement,
filesystem and database write amplification. Logical file growth is not TBW.
Record device age/health and an uncertainty range; no endurance prediction is
available before the pilot storage is identified. Do not select universal
percentage headroom or claim unlimited retention from these formulas.

The reference four-record outage test and 200-record intake workload establish
functional behavior only. Pending pilot inputs: named host, OS/filesystem, disk
model/endurance, expected asset/rate/cardinality, burst duration, allowed loss,
retention/outage targets and operational reserve. Capture whole-stack CPU/RSS,
disk bytes and physical writes alongside these calculation inputs.

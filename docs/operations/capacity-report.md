# Reference capacity measurements — pilot qualification pending

Hades-022 requires representative measurements on the intended pilot host. The
Siemens S7-1200 / Modbus TCP pilot is explicitly on hold; host specifications,
rates, signal map, disk model and retention/outage targets are unconfirmed.
No production sizing recommendation or completed ticket is claimed.

## Repeatable reference workload

```sh
go build -o artifacts/hades-capacity ./cmd/hades-capacity
artifacts/hades-capacity -host-label <machine-inventory-id> -count 200 -rate 50 > steady.json
artifacts/hades-capacity -host-label <machine-inventory-id> -count 200 -rate 0 > burst.json
```

The Go tool creates/removes its own private temporary queue; it never contacts a
PLC, broker or cloud endpoint. It validates simulator envelopes and syncs them
through the actual transfer intake. It reports environment/build identity,
process CPU time, process maximum RSS with OS-specific units, sampled Go heap,
file bytes, accounted queue bytes, record/payload/label cardinality, actual
throughput, schedule lag and p50/p95/p99 admission latency. It enforces 1–10,000
records and the production queue/disk-reserve limits. One producer is serial;
rate zero means unpaced serial intake, not concurrent traffic or maximum capacity.

Per-record runtime memory sampling adds overhead. No warmup, sustained soak,
confidence intervals, historian compaction, broker traffic or remote replay is
included. `uname` architecture and logical CPUs describe the host, but do not
identify CPU model, physical RAM, filesystem or storage endurance. Record those
from the actual pilot inventory; do not guess them from this reference run.

## Development reference, 2026-10-15

Committed JSON under `benchmarks/reference-{steady,burst}.json` records a macOS
arm64 development host with 12 logical CPUs, Go 1.27.2, one asset, one numeric
measurement, one numeric label set, 200 unique identities and mean payload
483.26 bytes. The base source revision is recorded; subsequent runs also embed Go build
information for the capacity tool (added after this initial measurement). These short runs are illustrative evidence.

| Measurement | Paced 50/s | Unpaced serial |
| --- | --- | --- |
| Observations | 200 | 200 |
| Duration (seconds) | 3.99 | 2.34 |
| Achieved records/second | 50.10 | 85.63 |
| Synced admission p95 (ms) | 14.89 | 14.69 |
| Synced admission p99 (ms) | 20.08 | 21.08 |
| Process CPU seconds | 0.369 | 0.316 |
| Process maximum RSS (MiB) | 18.06 | 17.81 |
| Queue file growth per record (bytes) | 971.67 | 971.68 |
| Accounted bytes per record, including 4 KiB charge | 5,067.67 | 5,067.68 |
| Refused observations | 0 | 0 |

At this measured envelope size only, the 256 MiB accounted queue budget would
bind at roughly 52,970 records before the 100,000-record limit. At 50 new records
per second, an initially empty queue gives about 1,059 seconds (17.7 minutes),
before metadata/status, suspended occupancy, rate variance and disk-reserve
constraints. This arithmetic is not a demonstrated outage guarantee. It does
not represent 72 hours of storage; replay age and capacity are separate limits.

Use [retention sizing](retention-sizing.md) for formulas and required inputs.
Pilot qualification still needs whole-stack steady/burst CPU/RAM, physical disk
writes/endurance, historian growth under actual label cardinality, acquisition
load, replay catch-up and longer observation intervals on the named target host.
Run with a trusted machine inventory label and preserve duration, source/build
revision, workload parameters and raw results. Hades-022 stays open.

## Multi-context reference extension

Hades-025's development harness now exercises actual normalizers, MQTT, historian
adapters/database, Go and C/Python consumers and optional durable receipt transfer.
See [workload commands and measurement limits](../../benchmarks/stack/README.md).
The serial transfer-intake results above retain their original narrow scope.
The harness supports bounded multi-measurement simulator contexts, declared
bursts and fixture outages. Its report lists omitted components, sampled timing
precision, illustrative sizing assumptions and missing physical metrics explicitly. Neither report qualifies the OPC UA pilot.

# Reference health and resource boundaries

Hades-016 adds diagnostics for the merged lab stack. No pilot hardware or real
signal map has been selected; limits below are configured reference limits, not
measured throughput, outage capacity or industrial deadline guarantees.

| Layer | Signals | Interpretation/action |
| --- | --- | --- |
| Device/driver | Gateway `acquired`, `acquisition_failures`, `acquisition_us_sum` | Increasing read failures with ingress working points to source/driver. Compare approved device/reference readings; no device access is authorized by the lab. |
| Gateway/ingress | Gateway `pending_records` (limit 1), `ingress_retries`, `admitted` | Pending+retries means delivery backpressure; no subsequent poll until admission. Failures create null/bad, clock freshness stays unknown. |
| Validation | Normalizer `/health`: `rejected`, `storage_failures`, quarantine counts | Invalid input is separate from storage failure; inspect private bounded diagnostics. |
| Processing | `processing_count`, `processing_ms_sum`, `stale_readings`, `unknown_freshness` | Average elapsed ingress processing time is sum/count, including admission/refusal work. Freshness counts validated reading attempts, including refused retries; not unique acquisitions. |
| MQTT publication | `publish_failures`, `retries`, pending records/bytes | Retry counts are failed publish attempts, including ambiguous acknowledgements; PUBACK is not downstream commit. Broker logs identify connection/auth and queue issues. |
| History | Periodic `history_stats`: pending/suspended, retries, write count/ms, validation/storage/capacity failures | Pending+retry suggests sink outage; suspended means permanent rejection/operator reconciliation. `api_accepted` is best-effort API acceptance. |
| Disk | History/diagnostic stats `free_bytes`, `reserve_bytes`, `disk_probe_ok`, `disk_reserve_ok`; normalizer `disk_reserve_ok` | Probe failure or reserve=false is an alert condition. Refuse new persistence instead of deleting owned data. |
| Transfer | `transfer_state=not_implemented` | Never infer cloud delivery from healthy local components. Hades-018/019 implement this stage. |

Normalizer health remains protected by the admin mTLS role in the secure profile.
History stats are local stdout JSON, not a public payload-reading API. Gateway
health is periodic diagnostic JSON on the configured log writer, independent of
blocked ingress attempts. All counters reset after restart; persistent store
occupancy/ownership is recovered. Unknown volatile restart losses stay unknown
unless an independent producer ledger measures them. Logs contain no private
keys or full acquisition payload; CLI stdout observation records are explicit
development evidence and must not be enabled as unbounded production logging.

## Limits and persistence

- Normalizer: 4,096 records / 16 MiB volatile pending+in-flight, HTTP slots 16,
  65,536-byte payloads; capacity gives 503, never silently evicts admitted work.
- Gateway lab: one volatile envelope, no overlapping/catch-up polls, minimum
  one-second interval, 200 ms request and two-second driver deadline.
- History: 100,000 records / 256 MiB synced intake+dedup; quarantine 10,000 /
  64 MiB with diagnostic eviction counted; persistence paths are explicit CLI
  `-intake`/`-quarantine`, private mode 0700/0600. Disk reserve is 128 MiB.
- Broker: queue record/byte limits, inflight/packet/client caps in pinned config;
  host log rotation and bounded memory/CPU are explicit in both Compose profiles.
- Secure Compose: broker 128 MiB/0.5 CPU, historian 256 MiB/1 CPU, proxy 64 MiB/
  0.5 CPU; runtime logs 5 MiB x 2. Persistence uses named broker/history volumes;
  do not use `down -v` on retained evaluation data.
- Host Go services support `-log-file /private/path/service.log`: two rotated
  files, at most 5 MiB each, mode0600, exclusive writer lock. Without this option,
  the supervisor must bound stderr. `deploy/systemd/hades-service@.service`
  illustrates CPUQuota=50%, MemoryMax=256M, TasksMax=64, private state/log paths
  and journal rate limiting. It is a provisioning template, not an installed
  service or a measured pilot allocation. Provision per-role users/credentials
  and directories before adapting it for site rollout. Binary stdout evidence
  is suppressed in this host service template.

The free-space check samples availability; it cannot reserve blocks against
other writers. Allocate separate host budgets for historian retention and intake,
and monitor filesystem/inode exhaustion. A disk probe is an alert signal, not
an OOM/disk failure prediction. No synthetic record counts replace unknown loss.

## Verification

Race tests inject normalizer full queue, invalid input, publish failure and
historian rejection/retry. The existing pinned-driver smoke injects timeout and
Modbus exception and compares null/bad behavior. Host log tests force rotation
under a small bound. `scripts/run-recovery` exercises abrupt process boundaries,
capacity refusal, diagnostic eviction and simulated disk reserve exhaustion in
private disposable state; it emits an ID comparison report. Linux CI repeats
these plus the existing TLS, independent-consumer and source-to-history checks.
Real pilot CPU/RAM/log/disk measurements and layer fault drills remain pending
Hades-014 hardware selection and Hades-022 measurement.

References: [systemd resource controls](https://github.com/systemd/systemd/blob/v259/man/systemd.resource-control.xml), [host execution/log controls](https://github.com/systemd/systemd/blob/v259/man/systemd.exec.xml).

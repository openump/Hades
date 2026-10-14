# Local recovery matrix: reference evidence

Hades-017 adds reproducible process-crash, capacity and backpressure checks.
Run from the checkout using `./scripts/run-recovery`. The default uses a new
private Docker Compose project and disposable volumes; it never inherits the
user's retained development project. Set `HADES_RECOVERY_STACK=1` to include
actual broker restart/duplicate and sink-outage/restart smoke checks. Reference
ports 18885/18886/18432 can be changed with `HADES_RECOVERY_MQTT_PORT`,
`HADES_RECOVERY_BLOCKED_PORT`, and `HADES_RECOVERY_VM_PORT` to avoid conflicts.

Native macOS verification accepts explicit `HADES_VM_BINARY` and
`HADES_MOSQUITTO_BINARY`; Compose-dependent restart smokes are then recorded as
not run, not silently counted as passing. Hosted Linux runs the complete path.
The report and external source ledgers are saved in `artifacts/recovery/` (ignored
by Git). CI prints the JSON report and test output. No production system, device
or disk-fill workload is contacted by this command.

| Scenario | Boundary and required outcome |
| --- | --- |
| Gateway killed while ingress returns 503 | External producer ID is known, gateway has one volatile copy, no acceptance: that ID is lost at kill. Runtime cannot reconstruct/count it from later polling. |
| Normalizer killed after volatile 202, before publish | External ID is admitted but absent after restart: report loss rather than invent recovery. |
| Publisher blocked, queue capacity reached | In-flight record continues occupying the one-record test queue. Next request receives 503; cancellation does not retire the owned record. |
| Synced file intake killed with unfinished temporary append | Original synced ID/body survives; uncommitted `.pending` is discarded. Corrupt committed records still fail startup. |
| Historian adapter killed after sync/before sink | Original ID/body recovers pending and writes once on recovery. |
| Historian adapter killed inside uncertain sink outcome | Original pending record survives and is replayed. This helper models an unknown outcome; it does not assert the sink durably committed it. |
| Quarantine reaches small test bound | Four diagnostics retain two, with exactly two counted evictions. Diagnostic loss never deletes owned intake. |
| Simulated reserve exhaustion | Set test store reserve above actual free space. New intake returns capacity error, owned synced record remains, reserve health is false. This tests admission logic, not ENOSPC/fsync failure or physical media behavior. |
| Non-acknowledging MQTT consumer and full broker queue | Dedicated broker has two queued records and Receive Maximum=1. One delivered message stays unacknowledged, ten further IDs publish while disconnected; resumed client receives a bounded subset. Report exact missing IDs: PUBACK is not downstream delivery. |
| Historian SIGKILL immediately after API acknowledgement | An external ledger maps 20 sample timestamps to IDs. Restart/query reports recovered/missing IDs; losses are evidence, not a failing zero-loss assertion. |
| Broker SIGKILL before autosave | Dedicated persistent broker uses a 3,600-second autosave interval, receives PUBACKed offline queue IDs, and is killed before autosave. Restart reports exact recovered/lost IDs without claiming the reference 5-second profile has the same window. |
| Broker restart and sink-outage restart | Existing smoke tests compare exact source/output IDs, duplicate handling and synced pending ownership with actual services. |

The native macOS run recovered synced intake across SIGKILL and reported both
volatile losses as expected. With a 30-second historian in-memory flush interval,
20/20 API-accepted test samples were absent after immediate termination. This
stress interval differs from the one-second reference Compose setting. Hosted
Linux tests the reference setting and reports its own result; neither result
establishes a universal maximum loss interval. The report records elapsed time
from the observed API response to termination request completion, not a storage
commit timestamp.

The blocked-client test records the broker's observed dropped IDs. It does not
assume oldest/newest drop behavior from a configuration cap. A consumer that
stops acknowledging or exceeds its queue must be treated as an observable loss
condition; successful source PUBACK cannot close the end-to-end gap.

## Remaining evidence and interpretation

The matrix checks software behavior on a reference host. Pilot device/firmware,
operator-approved signal scope and hardware remain unselected. Therefore it is
not pilot qualification and does not close Hades-014/015 or the hardware-specific
portion of Hades-016. Hades-022 must measure capacities/CPU/RAM/storage on named
hardware. Physical power interruption, filesystem/drive sync honesty, exact
fsync failures, reference broker autosave loss-window measurements and complete site-level loss windows
require further hardware/fault evidence; they are not inferred from SIGKILL.

The accepted historian profile remains best-effort API acceptance. Retained
`api_accepted` envelopes allow operator reconciliation within retention, but
there is no automatic database-loss repair or durable per-sample receipt.
Storage intake durability starts only after synced admission. Volatile acquisition
and publication queues are not retroactively made durable by later stages.
No exactly-once, zero-loss or outage-duration guarantee is established.

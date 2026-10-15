# Cloud-outage isolation and replay evidence

Hades-019 verifies the Hades-018 transfer queue with a contract-compatible Go
receiver fixture, independently of a deployed Zeus stack. These results are
lab evidence. No real pilot device, host, endpoint or access window is selected.

## Reproduce

Use a **disposable** reference broker, not a site/pilot broker. In Linux CI the
broker job supplies its own Compose project and published ports:

```sh
python3 -m pip install -r requirements-dev.txt
docker compose -f deploy/compose.yaml up --build -d --wait
./scripts/run-outage-scenario
```

The orchestrator uses the existing reference smoke's isolated temporary
normalizer/history stores and a loopback-only mTLS receiver. It removes child
processes and temporary credentials/stores. Its caller owns broker/Compose
cleanup. Use `COMPOSE_PROJECT_NAME`, `HADES_MQTT_PORT` and `HADES_VM_PORT` to avoid
other development stacks. The test-only fixed normalizer port is 18082; don't
run another normalizer there concurrently.

For native verification, provide `HADES_VM_BINARY`, `HADES_MQTT_PUB`,
`HADES_MQTT_SUB`, the two unique port variables and `HADES_PYTHON` pointing to the
virtual-environment interpreter. Start a disposable loopback broker first.
The source ledger/report is written to ignored
`artifacts/cloud-outage/report.json`; Go tests print the crash/disk matrix.

Selected versions: Go 1.27.2; Eclipse Paho Go v0.23.0; Bento v1.22.0;
Mosquitto 2.1.2; VictoriaMetrics community v1.154.0; Python 3.14.6 as test
orchestration only. No deployed custom service requires Python.

## Observed native result, 2026-10-15

The queue limit was four records (two metadata/status records and two
observations), 256 MiB accounted bytes and a 128 MiB free-disk reserve. The
third observation deliberately exceeded the record limit. This is a small
functional fault test, not a full-volume performance qualification.

| Fault / boundary | Measured result |
| --- | --- |
| Receiver unavailable (HTTP 503) | All 3 source observations reached synced local history and both independent Go/Paho and Python/Mosquitto consumers. |
| Local historian independently stopped/restarted | 3 pending observations recovered, then became API-accepted; exact numeric value/time and quality exports passed. |
| Transfer queue overflow | 2 observations queued, 1 explicitly refused for transfer; local loss 0. Source IDs ending 030/031 recovered remotely; 032 was the intentional transfer loss at the outage checkpoint. |
| Transfer killed during retry and restarted | All 4 owned records recovered from disk without changed topic/body/digest. |
| Receiver commits but receipt is lost | Source retained all 4 records; at least 1 receiver receipt committed before receiver SIGKILL. Restart/replay returned the same original receipt. |
| Receiver restored | Exactly the 4 queued identities arrived, including 2 observations; original bytes, timestamps, provenance and digests matched. Only verified receipts emptied the source queue. |
| Late observation replay | Original event times were at least 15 seconds old and remained unchanged; independent consumers agreed on stale freshness. |
| Source SIGKILL inside send callback | Separate race-tested child-process case recovered 1/1 original identity, loss 0. |
| Disk reserve exhaustion | Simulated reserve above available disk: 1 owned record preserved, 1 new delivery refused. No host disk was filled. |
| Byte capacity | Separate 4 KiB queue refused an observation even below its record limit. |
| 401/403 | Worker paused; original pending ownership preserved. |
| 409 / permanent rejection | Original record suspended and consumes queue capacity; explicit resume preserves identity/content. |
| Malformed/mismatched/duplicate-key/oversize receipts and 202/204 | Never treated as delivered; bounded retries preserve ownership. |
| Replay window exceeded | More than 72 hours of local ownership suspends for reconciliation; no event-time fabrication or silent deletion. |

The lost-ID report is a checkpoint before later duplicate injections. A later
manual/source retry can legitimately recover a delivery previously refused at
capacity; a process-lifetime drop counter is not a permanent unique-ID loss
ledger. The test compares source, local and receiver IDs/bytes to measure loss.

## Limits and remaining integration work

The lab tests exercise bounded admission and failure behavior. They do not
qualify default queue memory usage, a selected site's disk endurance, host CPU,
container limits, real network impairment or power-loss guarantees. The store
uses file/directory fsync on the local filesystem; replicated cloud durability
and hardware power safety require separate evidence. The receiver fixture has
no automatic retention eviction and is not a Zeus deployment.

A full source queue keeps suspended ownership; new transfer intake is lossy by
explicit policy. MQTT broker sessions have their own bounded retention and can
lose additional unacknowledged deliveries under prolonged adapter failure;
this experiment does not claim otherwise. Cloud transfer never owns local
history or consumer acknowledgements. Unknown/non-capacity storage failures
stop only the transfer adapter and leave its packet unacknowledged.

The approved metadata snapshot must remain available for replay/restart. A
multi-revision archive, deployed Zeus scope/receipt agreement, seven-day dedup
operations and downstream late-event/action policies remain integration work.
See [transfer implementation](../integration/zeus-transfer.md) and
[local recovery evidence](recovery-evidence.md).

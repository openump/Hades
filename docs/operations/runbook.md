# Reference operator runbook

## Clean-host evaluation

Use a disposable supported Linux amd64 host with Git, Docker Engine/Compose,
Go matching `go.mod`, and Python 3.11+ with `requirements-dev.txt` and
`deploy/opcua/requirements-pilot.txt` in a virtual environment. CI uses Ubuntu 24.04;
arm64 Go cross-builds are not an arm64 whole-stack runtime qualification.
Clone the approved source commit, preserve its hash, run `go mod verify`,
`go test -race ./...`, and `./scripts/check-contracts`.

Follow [identity setup](identity-networking.md) and
[configuration rollout](configuration-rollout.md) to build the Linux proxy,
prepare external private credentials, review a secure manifest, and validate it
before applying. Use a distinct Compose project and nonconflicting loopback
ports. Disposable `hades-pki` identities are verification certificates, not a
production CA service. Run `hades-site -action status` to verify actual running
revision labels, or use `hades-service` for native systemd digest-bound
supervision (`deploy/systemd/`).

Execute `scripts/smoke-security`, `scripts/smoke-ot-gateway` with the documented
pinned Telegraf fixture, `scripts/smoke-opcua-pilot`, `scripts/run-recovery`,
`scripts/run-outage-scenario`, and `scripts/smoke-site-recovery` only against
disposable test stacks. These scripts stop services, kill test processes, and
remove their own volumes; do not point them at a live plant deployment. Preserve
all JSON reports and external source ledgers.

## OPC UA mock software pilot (`Hades-014`, `Hades-015`, `Hades-022`)

The owner selected a local OPC UA mock server (`hades-opcua-mock`, `asyncua==1.1.6`)
and read-only Go gateway (`hades-opcua-gateway`) in place of the Siemens/Modbus
physical pilot. Follow the [OPC UA pilot profile](../pilot/opcua-profile.md),
[site profile](../pilot/site-profile.md), [read-only checklist](../pilot/read-only-checklist.md),
and [integration evidence](../pilot/integration-evidence.md).

1. **End-to-end OPC UA software pilot smoke**:
   ```sh
   ./scripts/smoke-opcua-pilot
   ```
   Verifies `Basic256Sha256` `SignAndEncrypt` session establishment, read-only user
   certificate enforcement (`BadUserAccessDenied` on writes), versioned NodeId map
   (`press-04/v1`), quality/timestamp mapping (`good`, `bad`, `uncertain`), dual
   independent MQTT consumers (Go/Paho and Python/Mosquitto), VictoriaMetrics numeric
   history, and fault-recovery scenarios.
2. **Whole-stack OPC UA pilot and multi-context capacity & retention qualification**:
   ```sh
   ./scripts/benchmark-stack --opcua-pilot --output-dir <report-dir>
   ./scripts/benchmark-stack --output-dir <report-dir>
   ```
   Executes steady and burst runs across the live OPC UA pilot path and multi-context
   topologies (`single-asset`, `two-assets`, `four-assets`), validating zero silent loss
   and Tier 2 IPC retention/endurance budgets documented in
   [capacity-report.md](capacity-report.md) and [retention-sizing.md](retention-sizing.md).

Physical PLC access and controller/firmware qualification remain outside this software pilot.

## Troubleshooting and recovery

| Symptom | First check / response |
| --- | --- |
| No observations | Gateway acquisition/error counters (`hades-opcua-gateway` / `hades-gateway`), approved metadata, and normalizer admission; compare external source ledger. |
| Rejected values | Schema/context diagnostics and mapping revision; never replace missing/bad values with zero. |
| History missing after API success | Declared best-effort historian boundary, time range/quality, and `hades-history` WAL segment state; inspect recovery evidence before asserting durability. |
| Transfer backlog | Queue/reserve in `hades-transfer` WAL segments (`segment-*.wal`), receipt errors, authenticated endpoint, and suspended records. Reconcile before resuming identities; never regenerate IDs to bypass conflicts. |
| Authorization pause | Certificate trust/expiry (`hades-pki` / OPC UA X.509) and enterprise/topic scope; repair credentials and restart transfer or gateway. |
| Revision mismatch | Inspect live labels (`hades-site -action status` or `hades-service`); apply can partially succeed. Use explicit compatible rollback; do not trust only the stored marker. |
| Disk pressure | Refuse new intake (`MaxBytes` / `ReserveBytes`), preserve committed `segment-*.wal` frames, inspect per-store bounds and backup staging. Never delete active `segment-*.wal` files to clear an alarm. |

Follow [diagnostics](diagnostics.md), [quarantine](quarantine.md),
[rollback](configuration-rollout.md), and [cold restore](backup-restore.md).
Both `hades-transfer` and `hades-history` persist queue and dedup state in
append-only CRC32C-framed WAL segments (`segment-*.wal`) with backward-compatible
automatic migration from legacy v1 JSON state directories. Stop writers before
backup, declare every host state directory, and verify restored samples/IDs on
isolated volumes with separately provisioned credentials. Record measured
recovery time and missing source IDs; never infer zero loss.

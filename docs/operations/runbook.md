# Reference operator runbook

## Clean-host evaluation

Use a disposable supported Linux amd64 host with Git, Docker Engine/Compose,
Go matching go.mod and optional Python reference tooling. CI uses Ubuntu 24.04;
arm64 Go cross-builds are not an arm64 whole-stack runtime qualification.
Clone the approved source commit, preserve its hash, run `go mod verify`,
`go test -race ./...` and `./scripts/check-contracts`. Optional independent Python
checks require `requirements-dev.txt` in a virtual environment.

Follow [identity setup](identity-networking.md) and
[configuration rollout](configuration-rollout.md) to build the Linux proxy,
prepare external private credentials, review a secure manifest and validate it
before applying. Use a distinct Compose project and nonconflicting loopback
ports. Disposable `hades-pki` identities are verification certificates, not a
production CA service. Run `hades-site -action status` to verify actual running
revision labels. Independently launched host adapters need their own lifecycle.

Execute `scripts/smoke-security`, `scripts/smoke-ot-gateway` with the documented
pinned Telegraf fixture, `scripts/run-recovery`, `scripts/run-outage-scenario`
and `scripts/smoke-site-recovery` only against disposable test stacks. These
scripts stop services, kill test processes and remove their own volumes; don't
point them at a pilot deployment. See their individual guides for environment
variables and native/Compose prerequisites. Preserve reports and source IDs.

## Read-only Siemens pilot — on hold

Selected direction is Siemens S7-1200 via Modbus TCP. Exact CPU/firmware,
MB_SERVER configuration, signal/register layout, polling limits, host and access
authorization remain unconfirmed. Use the [read-only checklist](../pilot/read-only-checklist.md)
and [site profile](../pilot/site-profile.md). Current gateway CLI is lab-only and
rejects non-loopback sources; do not bypass that restriction to connect a PLC.
A reviewed device-specific implementation/configuration and operator approval
must precede site access. No PLC program download is authorized by this package.

## Troubleshooting and recovery

| Symptom | First check / response |
| --- | --- |
| No observations | Gateway acquisition/error counters, approved metadata and normalizer admission; compare external source ledger. |
| Rejected values | Schema/context diagnostics and mapping revision; never replace missing/bad values with zero. |
| History missing after API success | Declared best-effort historian boundary, time range/quality and adapter state; inspect recovery evidence before asserting durability. |
| Transfer backlog | Queue/reserve, receipt errors, authenticated endpoint and suspended records. Reconcile before resuming identities; never regenerate IDs to bypass conflicts. |
| Authorization pause | Certificate trust/expiry and enterprise/topic scope; repair credentials and restart transfer. |
| Revision mismatch | Inspect live labels; apply can partially succeed. Use explicit compatible rollback; don't trust only the stored marker. |
| Disk pressure | Refuse new intake, preserve committed records, inspect per-store bounds and backup staging. Don't delete owned queue records to clear an alarm. |

Follow [diagnostics](diagnostics.md), [quarantine](quarantine.md),
[rollback](configuration-rollout.md) and [cold restore](backup-restore.md).
Stop writers before backup, declare every host state directory and verify
restored samples/IDs on isolated volumes with separately provisioned credentials.
Record measured recovery time and missing source IDs; never infer zero loss.

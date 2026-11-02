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

## Security-control evidence pack (`Hades-037`)

`hades-check -cra-report` evaluates a reviewed secure release and writes an
evidence pack for a CRA / IEC 62443-4-2 assessment. **It is not a conformity
assessment, CE marking or certification**; every output says so.

```sh
go run ./cmd/hades-site -action prepare -root "$RELEASE" -revision site-a \
  -project hades-site-a -profile secure -security-dir "$HADES_SECURITY_DIR" > "$RELEASE/site.json"
go run ./cmd/hades-check -root "$RELEASE" -cra-report -site-config site.json \
  -out-dir evidence/site-a -binaries artifacts/hades-history-proxy,artifacts/hades-normalizer \
  -policy /etc/hades/actuation-policy.json
```

It writes `sbom.cdx.json` (CycloneDX 1.5, built from the module list embedded
in the given deployed binaries, so it lists exactly what is linked, with go.sum
SHA-256 hashes and reviewed license identifiers), `cra-attestation.json` and
`cra-compliance-report.md`, and exits non-zero unless every mandatory control
passes and no control fails:

| Control | IEC 62443-4-2 | Mandatory |
| --- | --- | --- |
| `transport-tls13`: every broker listener has a certificate, `tls_version tlsv1.3` and required client certificates | CR 1.1, CR 3.1 | yes |
| `identity-no-anonymous`: no anonymous access, client certificate required, identity as username, no static ACL file, global (not per-listener) settings | CR 1.1, CR 1.2 | yes |
| `identity-certificate-validity`: every certificate in the manifest's credential directory is in its validity window and chains to that directory's `ca.crt` (other CA certificates are never trust anchors) | CR 1.2, CR 1.9 | yes |
| `authz-least-privilege`: default deny; no `#` and no `+` before the last level; only `hades-actuator`, `command-proposer` and `hades-mcp` may publish any filter that can match a command or receipt topic; only `admin` may publish `$CONTROL` | CR 2.1 | yes |
| `integrity-artifact-digests`: SHA-256 of each deployed binary, each with readable Go build information for the SBOM | CR 3.4 | yes |
| `integrity-site-configuration`: secure manifest verified against the release files (`site.Validate`); revision and manifest digest recorded | CR 3.4 | yes |
| `resilience-resource-limits`: bounded broker queues, inflight, packet and message sizes, clients; control API off | CR 7.1 | yes |
| `integrity-actuation-policy`: policy revision and SHA-256 recorded; policies are unsigned, so signature status is `NOT_ASSESSED` | CR 3.4 | no |
| `resilience-runtime-guardrails`: WAL bounds and PLC rate limits are runtime flags; verify on the node | CR 2.8, CR 7.1 | no |

`NOT_ASSESSED` is never counted as a pass. The Go services' TLS 1.3 floor is a
property of the source build (`internal/transport`), not inferred from binary
digests. Runtime container image digests are
not part of the SBOM: the reference broker and historian images are built
locally from `deploy/*/Dockerfile`, whose pinned bases the manifest digests.

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

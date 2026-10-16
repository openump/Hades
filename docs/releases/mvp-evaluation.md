# Hades MVP evaluation package — reference candidate

**Disposition: Software MVP reference candidate complete (`Hades-001` through `Hades-023`); not yet accepted for a production plant rollout.**
This package maps reference implementation evidence to [WHITEPAPER.md](../../WHITEPAPER.md) section 9. It is not a release tag, open-source license grant, operator sign-off, or proof of physical PLC/IPC hardware compatibility.

## White-paper section 9 evaluation matrix

| White-paper criterion | Software reference evidence (`main`) | Remaining physical / governance gate |
| --- | --- | --- |
| 1. Clean-host stack from pinned artifacts | Linux CI builds and starts disposable pinned stacks (`deploy/compose*.yaml`, `deploy/systemd/*.service`, 13 Go CLIs, 7 `schemas/v1` contracts / 94 fixtures). Runbook verified on clean host (`Hades-001`..`Hades-021`, `Hades-028`, `Hades-032`). | Execute runbook on the target site host and record site acceptance. |
| 2. Simulator and supported OT source | Simulator, four-poll FC3 Modbus lab (`scripts/smoke-ot-gateway`), and pinned read-only OPC UA software pilot (`hades-opcua-mock` + `hades-opcua-gateway`, `press-04/v1`, `Basic256Sha256` `SignAndEncrypt`, `scripts/smoke-opcua-pilot`) pass (`Hades-004`, `Hades-013`, `Hades-014`, `Hades-015`). | Physical PLC/controller acceptance when physical OT hardware is introduced (`Hades-031`). |
| 3. Two independent consumers | Go/Paho and Python/Mosquitto agreement checked by `scripts/smoke-stack`, `scripts/smoke-opcua-pilot`, and cloud-outage CI (`Hades-011`). | None for software MVP baseline. |
| 4. Bounded invalid-input quarantine | `internal/normalize` tests, bounded quarantine ring/counters, and normalization/security/recovery CI (`Hades-009`, `Hades-016`). | None for software MVP baseline. |
| 5. Queryable numeric history | `scripts/smoke-history` and `scripts/smoke-opcua-pilot`: value/time/quality, query range, WAL-backed intake (`segment-*.wal`), and replay checks (`Hades-010`, `Hades-026`). VictoriaMetrics HTTP acknowledgement remains best effort. | None for software MVP baseline. |
| 6. Deduplication and restart | Local crash (`scripts/run-recovery`) and cloud receipt (`scripts/run-outage-scenario`) matrices with CRC32C-framed segmented WAL (`internal/storage`), compact in-memory index, original IDs/bytes, and explicit loss ledger (`Hades-006`, `Hades-017`, `Hades-019`, `Hades-026`). | Optional pre-MQTT normalizer ingress durability (`Hades-029`) if zero volatile window is required across simultaneous broker+normalizer crash. |
| 7. Cloud-outage isolation | `scripts/run-outage-scenario` and `scripts/benchmark-stack --opcua-pilot`: local consumers and historian continue while `hades-transfer` WAL buffers/replays or refuses newest at capacity (`Hades-018`, `Hades-019`, `Hades-022`, `Hades-026`). | Agree deployed Zeus production endpoint, identity, and receipt replay policy. |
| 8. Authentication and authorization | `scripts/smoke-security` and `scripts/smoke-opcua-pilot`: segmented mTLS, scoped SUBACK/publish/management denials, OPC UA read-only X.509 user policy (`BadUserAccessDenied` on write), and CA replacement (`Hades-012`, `Hades-015`, `Hades-024`). | Production PKI provisioning and multi-context certificate SAN/CommonName mapping (`Hades-027`). |
| 9. Config validate/deploy/runtime revision/rollback | `scripts/smoke-site-recovery` and `internal/supervisor` tests: `openump-site/1` manifest digest validation, native systemd (`hades-service`) and Compose revision checks, rejected change, and preserved history on rollback (`Hades-020`, `Hades-021`, `Hades-028`, `Hades-030`). | Validate stop/rollback/restore on target site host. |
| 10. Named-hardware sizing and loss assumptions | Whole-stack OPC UA pilot (`--opcua-pilot`) and multi-context (`single-asset`, `two-assets`, `four-assets`) capacity, latency, retention, outage, and endurance bounds measured and documented against the Tier 2 Reference IPC profile in [capacity-report.md](../operations/capacity-report.md) and [retention-sizing.md](../operations/retention-sizing.md), with segmented WAL eliminating per-record JSON rewrite amplification (`Hades-022`, `Hades-025`, `Hades-026`). | Validate physical SSD SMART endurance on physical Tier 2 plant IPC and record operator sign-off (`Hades-023`). |

## Evidence index

- [Protocol contracts and conformance (`7` schemas, `94` fixtures)](../protocol/conformance.md)
- [Actuation and command control-plane contracts](../protocol/actuation-control-plane.md)
- [OPC UA software pilot profile](../pilot/opcua-profile.md) and [integration evidence](../pilot/integration-evidence.md)
- [Local crash boundaries and WAL durability](../operations/recovery-evidence.md)
- [Cloud outage and receipt evidence](../operations/cloud-outage-evidence.md)
- [Configuration rollout and systemd supervision](../operations/configuration-rollout.md)
- [Backup and restore](../operations/backup-restore.md)
- [Capacity scope and whole-stack measurements](../operations/capacity-report.md)
- [Retention and outage sizing worksheet](../operations/retention-sizing.md)
- [Operating runbook](../operations/runbook.md)
- [Artifact inventory](artifact-inventory.md)

## Software verification checklist (completed in repository & CI)

- [x] Verify offline contract conformance across all 7 `schemas/v1` schemas and 94 fixtures (`./scripts/check-contracts`, `Hades-002`, `Hades-032`).
- [x] Verify Linux amd64/arm64 Go cross-builds (`go build ./cmd/...`) and race-enabled unit/integration suite (`go test -race ./...`).
- [x] Verify segmented mTLS, broker ACLs, and historian proxy authorization (`./scripts/smoke-security`, `Hades-012`, `Hades-024`).
- [x] Verify pinned OPC UA mock (`hades-opcua-mock`), versioned node map (`press-04/v1`), read-only X.509 identity, `Basic256Sha256` `SignAndEncrypt` security, bounded acquisition, and source-to-history fault scenarios (`./scripts/smoke-opcua-pilot`, `Hades-014`, `Hades-015`).
- [x] Verify local crash recovery (`./scripts/run-recovery`), cloud outage/receipt replay (`./scripts/run-outage-scenario`), and cold backup/restore + revision rollback (`./scripts/smoke-site-recovery`, `Hades-017`, `Hades-019`, `Hades-020`, `Hades-021`).
- [x] Verify append-only CRC32C-framed segmented WAL storage (`segment-*.wal`) and compact in-memory index in `hades-transfer` and `hades-history`, including legacy v1 JSON directory auto-migration (`Hades-026`).
- [x] Run repeatable steady/burst whole-stack software pilot (`--opcua-pilot`) and multi-context capacity measurements and document Tier 2 IPC retention, outage, and endurance bounds (`./scripts/benchmark-stack --opcua-pilot`, `Hades-022`, `Hades-025`).

## Owner, governance, and physical-pilot sign-off checklist (external gates)

- [ ] Owner selects project license (`ADR-0001`), adds `LICENSE`, and reviews dependency redistribution notices/SBOM.
- [ ] Reviewer accepts final evaluation PR and records immutable source commit/tag and binary SHA-256 digests.
- [ ] Review production credential provisioning, CA hierarchy, expiry/rotation, and scoped access outside Git.
- [ ] Validate physical disk write amplification and retention/outage budgets on the physical Tier 2 plant IPC and agree acceptable loss.
- [ ] Agree deployed Zeus endpoint, identity, receipt durability, and replay policy if cloud interoperability is included in the pilot.
- [ ] Operator records review, date, scope, restrictions, and approval reference below before production pilot rollout (`Hades-023`).

## Operator review and sign-off record

| Field | Value |
| --- | --- |
| Evaluation candidate scope | Software MVP + OPC UA mock software pilot (`Hades-001`..`Hades-023`, `Hades-024`..`Hades-026`, `Hades-028`, `Hades-030`, `Hades-032`) |
| Project license status | Pending owner selection ([ADR-0001](../decisions/0001-license-policy.md)) |
| Operator / reviewer name | _Pending operator sign-off_ |
| Review date | _Pending_ |
| Approved deployment scope | _Not approved for production plant rollout (local/CI software evaluation only)_ |
| Explicit restrictions | Read-only OT acquisition only; disposable `hades-pki` certs excluded from production; physical Tier 2 IPC and Zeus production endpoint sign-off pending |

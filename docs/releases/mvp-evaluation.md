# Hades MVP evaluation package — reference candidate

**Disposition: not accepted for a production pilot.** This package maps reference
implementation evidence to WHITEPAPER.md section 9. It is not a release tag,
license grant, operator sign-off or proof of physical hardware compatibility.

| White-paper criterion | Evidence / unresolved gate |
| --- | --- |
| 1. Clean-host stack from pinned artifacts | Linux CI builds and starts disposable pinned stacks. Run the runbook on the selected host; site acceptance pending. |
| 2. Simulator and supported OT source | Simulator, four-poll FC3 Modbus lab, and pinned read-only OPC UA software pilot (`hades-opcua-mock` + `hades-opcua-gateway`, `press-04`, `Aes256_Sha256_RsaPss` `SignAndEncrypt`, `scripts/smoke-opcua-pilot`) pass. Physical-device acceptance is separate. |
| 3. Two independent consumers | Go/Paho and Python/Mosquitto agreement checked by `scripts/smoke-stack`, `scripts/smoke-opcua-pilot`, and cloud-outage CI. |
| 4. Bounded invalid-input quarantine | `internal/normalize` tests and normalization/security/recovery CI. |
| 5. Queryable numeric history | `scripts/smoke-history` and `scripts/smoke-opcua-pilot`: value/time/quality, query range and replay checks. API acknowledgement remains best effort. |
| 6. Deduplication and restart | Local crash and cloud receipt matrices; original IDs/bytes and explicit loss ledger. |
| 7. Cloud-outage isolation | `scripts/run-outage-scenario` and `scripts/benchmark-stack --opcua-pilot`: local consumers/history continue while transfer queue buffers/replays or refuses newest at capacity. |
| 8. Authentication and authorization | `scripts/smoke-security` and `scripts/smoke-opcua-pilot`: mTLS, scoped SUBACK/publish/management denials, OPC UA read-only security policy, and CA replacement. Production PKI pending. |
| 9. Config validate/deploy/runtime revision/rollback | `scripts/smoke-site-recovery`: secure Compose revision checks, rejected change and preserved history on rollback. Independent host adapters covered by `scripts/benchmark-stack`. |
| 10. Named-hardware sizing and loss assumptions | Whole-stack OPC UA pilot (`--opcua-pilot`) and multi-context (`two-assets`, `four-assets`) capacity, latency, retention, outage, and endurance bounds measured and documented against the Tier 2 Reference IPC profile in [capacity-report.md](../operations/capacity-report.md) and [retention-sizing.md](../operations/retention-sizing.md). Physical Tier 2 IPC validation and operator sign-off (`Hades-023`) pending. |

## Evidence index

- [Local crash boundaries](../operations/recovery-evidence.md)
- [Cloud outage/receipt evidence](../operations/cloud-outage-evidence.md)
- [Configuration rollout](../operations/configuration-rollout.md)
- [Backup and restore](../operations/backup-restore.md)
- [Capacity scope and measurements](../operations/capacity-report.md)
- [Retention and outage sizing worksheet](../operations/retention-sizing.md)
- [Operating runbook](../operations/runbook.md)
- [Artifact inventory](artifact-inventory.md)

## Release and operator checklist

- [ ] Owner selects project license and reviews redistribution notices/SBOM.
- [ ] Reviewer accepts implementation PRs and records source commit/tag.
- [ ] Build artifacts on clean supported Linux host; record architecture and hashes.
- [ ] Run conformance, security, local/cloud recovery and site restore checks.
- [ ] Review private credential provisioning, expiry/rotation and scoped access.
- [x] Verify the pinned OPC UA mock, versioned node map, read-only identity,
      security, bounded acquisition and source-to-history fault scenarios (`Hades-014`, `Hades-015`).
- [x] Run repeatable steady/burst whole-stack software pilot (`--opcua-pilot`) and
      multi-context capacity measurements and document Tier 2 IPC retention, outage,
      and endurance bounds (`Hades-022`).
- [ ] Validate physical disk write amplification and retention/outage budgets on
      the physical Tier 2 plant IPC and agree acceptable loss.
- [ ] Validate stop procedure, rollback compatibility and isolated restore.
- [ ] Agree deployed Zeus endpoint, identity, receipt durability and replay policy
      if cloud interoperability is included in the pilot.
- [ ] Operator records review, date, scope, restrictions and approval reference
      before production pilot rollout (`Hades-023`).

No operator sign-off has been obtained. Software pilot implementation (`Hades-014`,
`Hades-015`) and repeatable whole-stack capacity and retention sizing (`Hades-022`)
are complete with explicit bounds; `Hades-023` remains open for operator review and
pilot readiness sign-off.

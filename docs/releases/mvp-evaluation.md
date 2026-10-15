# Hades MVP evaluation package — reference candidate

**Disposition: not accepted for a production pilot.** This package maps reference
implementation evidence to WHITEPAPER.md section 9. It is not a release tag,
license grant, operator sign-off or proof of physical hardware compatibility.

| White-paper criterion | Evidence / unresolved gate |
| --- | --- |
| 1. Clean-host stack from pinned artifacts | Linux CI builds and starts disposable pinned stacks. Run the runbook on the selected host; site acceptance pending. |
| 2. Simulator and supported OT source | Simulator and four-poll FC3 Modbus lab pass. Active pilot changed to OPC UA mock; server/node map, gateway integration and verification pending. Physical-device acceptance is separate. |
| 3. Two independent consumers | Go/Paho and Python/Mosquitto agreement checked by `scripts/smoke-stack` and cloud-outage CI. |
| 4. Bounded invalid-input quarantine | `internal/normalize` tests and normalization/security/recovery CI. |
| 5. Queryable numeric history | `scripts/smoke-history`: value/time/quality, query range and replay checks. API acknowledgement remains best effort. |
| 6. Deduplication and restart | Local crash and cloud receipt matrices; original IDs/bytes and explicit loss ledger. |
| 7. Cloud-outage isolation | `scripts/run-outage-scenario`: local consumers/history continue while transfer queue refuses newest at capacity. |
| 8. Authentication and authorization | `scripts/smoke-security`: mTLS, scoped SUBACK/publish/management denials and CA replacement. Production PKI pending. |
| 9. Config validate/deploy/runtime revision/rollback | `scripts/smoke-site-recovery`: secure Compose revision checks, rejected change and preserved history on rollback. Independent host adapters excluded. |
| 10. Named-hardware sizing and loss assumptions | Reference measurements and sizing worksheet exist; pilot CPU/RAM/disk/rates/endurance and representative whole-stack measurements pending. |

## Evidence index

- [Local crash boundaries](../operations/recovery-evidence.md)
- [Cloud outage/receipt evidence](../operations/cloud-outage-evidence.md)
- [Configuration rollout](../operations/configuration-rollout.md)
- [Backup and restore](../operations/backup-restore.md)
- [Capacity scope and measurements](../operations/capacity-report.md)
- [Operating runbook](../operations/runbook.md)
- [Artifact inventory](artifact-inventory.md)

## Release and operator checklist

- [ ] Owner selects project license and reviews redistribution notices/SBOM.
- [ ] Reviewer accepts implementation PRs and records source commit/tag.
- [ ] Build artifacts on clean supported Linux host; record architecture and hashes.
- [ ] Run conformance, security, local/cloud recovery and site restore checks.
- [ ] Review private credential provisioning, expiry/rotation and scoped access.
- [ ] Verify the pinned OPC UA mock, versioned node map, read-only identity,
      security, bounded acquisition and source-to-history fault scenarios.
- [ ] Run representative steady/burst whole-stack pilot capacity measurements.
- [ ] Set retention, outage tolerance, backup frequency and recovery objectives
      from measured storage/throughput/endurance and agreed acceptable loss.
- [ ] Validate stop procedure, rollback compatibility and isolated restore.
- [ ] Agree deployed Zeus endpoint, identity, receipt durability and replay policy
      if cloud interoperability is included in the pilot.
- [ ] Operator records review, date, scope, restrictions and approval reference
      before production pilot rollout.

No operator sign-off has been obtained. The owner replaced the Siemens pilot with a software OPC UA mock pilot;
its implementation and verification remain pending. Hades-022/023 remain open for those acceptance gates; passing lab
CI alone cannot close them or mark the Hades MVP production ready.

# OpenUMP — Hades

Hades is the industrial edge stack of OpenUMP (Open Universal Manufacturing Protocol). It acquires read-only machine observations, normalizes them, publishes contextualized data over MQTT, and supports local numeric history, append-only segmented WAL durability, outbound cloud transfer, and write-control-plane contracts.

This repository contains the [white paper](WHITEPAPER.md), the complete software MVP reference implementation (`Hades-001` through `Hades-023`), the OPC UA software pilot (`hades-opcua-mock` + `hades-opcua-gateway`), and the whole-stack capacity and Tier 2 IPC retention qualification package, licensed under **Apache License 2.0** ([LICENSE](LICENSE), [ADR-0001](docs/decisions/0001-license-policy.md)). The Software Pilot MVP baseline is signed off in the [MVP evaluation package](docs/releases/mvp-evaluation.md); physical plant commissioning remains gated on site-specific PLC/IPC hardware validation and production PKI.

## Implementation language

Custom deployed services and operational CLI tools use **Go**. Reuse the selected upstream components and declarative configuration for their existing capabilities. Python is development tooling for contract tests, the local OPC UA mock pilot server, and capability probes; deployed custom edge services do not require it. See [ADR-0003](docs/decisions/0003-runtime-language.md). The conformance implementation pins its Go toolchain and dependencies in `go.mod`/`go.sum`.

## Development setup

Go 1.27.2 is pinned in `go.mod` for conformance tooling and edge services. Go 1.21+ can automatically obtain the pinned toolchain. Python 3.11+ is optional development reference and mock-server tooling. Runtime probe prerequisites are documented alongside each probe.

```sh
git clone https://github.com/openump/Hades.git
cd Hades
cp .env.example .env
git diff --check
./scripts/check-contracts
go test ./...
# Optional Python development reference and OPC UA mock prerequisites:
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements-dev.txt -r deploy/opcua/requirements-pilot.txt
.venv/bin/python -m unittest discover -s tests -v
```

The `.env` file is ignored and contains development identifiers only. Never commit device credentials, private keys, site exports, or runtime data.

## MQTT development deployment

Start the pinned local broker and run its restart probe using [the development guide](docs/tutorials/mqtt-development.md). This development profile is localhost-only with shared anonymous permissions; production identity and segmented mTLS use [identity and networking](docs/operations/identity-networking.md).

## Synthetic telemetry and local slice

Run the Go simulator and validating consumer using [the sample guide](docs/tutorials/simulated-telemetry.md), or run the end-to-end local stack with [the local slice tutorial](docs/tutorials/local-slice.md).

## Contracts and capability checks

- [Namespace and identity](docs/protocol/namespace.md)
- [Telemetry contract](docs/protocol/telemetry.md) and [versioning](docs/protocol/versioning.md)
- [Metadata and producer status](docs/protocol/metadata-status.md)
- [Actuation and command control-plane contracts](docs/protocol/actuation-control-plane.md)
- [Delivery and bounded buffering](docs/protocol/delivery.md), [durability decision](docs/decisions/0004-delivery-durability.md), and [Zeus receipt proposal](docs/integration/zeus-transfer.md)
- [Selected dependency inventory](docs/dependencies.md) and [native probes](examples/component-probes/README.md)

Run `./scripts/check-contracts` for the offline Go conformance CLI across all seven `schemas/v1` contracts (`telemetry`, `asset-metadata`, `producer-status`, `transfer-receipt`, `command`, `command-receipt`, `actuation-policy`) and 94 fixtures. [Conformance documentation](docs/protocol/conformance.md) describes fixture checks, review baselines, and CI.

## Planning and contributions

Implementation tickets live in [Hermes](https://github.com/openump/Hermes/blob/main/plans/hades/PROJECT_PLAN.md). Reference their stable `Hades-` IDs in commits and implementation PRs. See [CONTRIBUTING.md](CONTRIBUTING.md) for verification and decision conventions.

## License

Hades is licensed under the **Apache License 2.0** ([LICENSE](LICENSE)). See [ADR-0001](docs/decisions/0001-license-policy.md) and [dependency notices](docs/dependencies.md) for third-party dependency licenses and redistribution requirements.

## Architecture and operations index

- Raw ingress, scaling and bounded diagnostics: [normalization and quarantine](docs/operations/quarantine.md)
- Numeric ingestion, synced intake and outage recovery: [history mapping](docs/protocol/history-mapping.md)
- Service identities and segmented TLS profile: [identity and networking](docs/operations/identity-networking.md)
- Pinned read-only OPC UA software pilot and Modbus lab evidence: [OPC UA pilot profile](docs/pilot/opcua-profile.md), [read-only checklist](docs/pilot/read-only-checklist.md), and [integration evidence](docs/pilot/integration-evidence.md)
- Operational counters/limits and segmented WAL (`segment-*.wal`) durability: [diagnostics](docs/operations/diagnostics.md) and [recovery evidence](docs/operations/recovery-evidence.md)
- Cloud transfer with durable receipts and isolated test receiver: [transfer implementation](docs/integration/zeus-transfer.md) and [cloud outage evidence](docs/operations/cloud-outage-evidence.md)
- Reviewed reference configuration revisions, systemd supervisor (`hades-service`), and cold recovery: [rollout](docs/operations/configuration-rollout.md), [backup/restore](docs/operations/backup-restore.md), and [operator runbook](docs/operations/runbook.md)
- Whole-stack OPC UA pilot (`--opcua-pilot`), multi-context capacity measurements, and Tier 2 IPC retention/endurance sizing: [capacity report](docs/operations/capacity-report.md) and [retention sizing](docs/operations/retention-sizing.md)
- Current evaluation status, artifact inventory, operator checklist and sign-off gates: [MVP evaluation package](docs/releases/mvp-evaluation.md) and [artifact inventory](docs/releases/artifact-inventory.md)

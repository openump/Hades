# OpenUMP — Hades

Hades is the industrial edge stack of OpenUMP (Open Universal Manufacturing Protocol). It acquires read-only machine observations, normalizes them, publishes contextualized data over MQTT, and supports local numeric history and outbound cloud transfer.

This repository currently contains the [white paper](WHITEPAPER.md) and foundation work. A production deployment is not yet available.

## Implementation language

Custom deployed services and operational CLI tools use **Go**. Reuse the selected upstream components and declarative configuration for their existing capabilities. Python is development tooling for contract tests and capability probes; deployed custom services must not require it. See [ADR-0003](docs/decisions/0003-runtime-language.md). The conformance implementation pins its Go toolchain and dependencies in go.mod/go.sum.

## Development setup

Go 1.27.2 is pinned in go.mod for conformance tooling. Go 1.21+ can automatically obtain the pinned toolchain. Python 3.11+ is optional development reference tooling. Runtime probe prerequisites are documented alongside each probe when introduced.

```sh
git clone https://github.com/openump/Hades.git
cd Hades
cp .env.example .env
git diff --check
./scripts/check-contracts
go test ./...
# Optional Python development reference:
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements-dev.txt
.venv/bin/python -m unittest discover -s tests -v
```

The `.env` file is ignored and contains development identifiers only. Never commit device credentials, private keys, site exports, or runtime data.

## MQTT development deployment

Start the pinned local broker and run its restart probe using [the development guide](docs/tutorials/mqtt-development.md). This development profile is localhost-only with shared anonymous permissions; production identity and TLS come later.

## Synthetic telemetry

Run the Go simulator and validating consumer using [the sample guide](docs/tutorials/simulated-telemetry.md). The example covers metadata/status, normal and bad readings, null missing readings, and retry/duplicate handling. It does not connect to machines or store history yet.

## Contracts and capability checks

- [Namespace and identity](docs/protocol/namespace.md)
- [Telemetry contract](docs/protocol/telemetry.md) and [versioning](docs/protocol/versioning.md)
- [Metadata and producer status](docs/protocol/metadata-status.md)
- [Delivery and bounded buffering](docs/protocol/delivery.md), [durability decision](docs/decisions/0004-delivery-durability.md), and [Zeus receipt proposal](docs/integration/zeus-transfer.md)
- [Selected dependency inventory](docs/dependencies.md) and [native probes](examples/component-probes/README.md)

Run `./scripts/check-contracts` for the offline Go conformance CLI. [Conformance documentation](docs/protocol/conformance.md) describes fixture checks, review baselines, and CI. Probe results cover native loopback capabilities, not a deployed stack.

## Planning and contributions

Implementation tickets live in [Hermes](https://github.com/openump/Hermes/blob/main/plans/hades/PROJECT_PLAN.md). Reference their stable `Hades-` IDs in commits and implementation PRs. See [CONTRIBUTING.md](CONTRIBUTING.md) for verification and decision conventions.

## License status

The owner has not yet selected a project license. No open-source license grant is asserted by this repository until that decision is recorded and a `LICENSE` file is added. See [ADR-0001](docs/decisions/0001-license-policy.md). The intended open-source reference stack requires explicit review of each dependency's exact edition and license.

Raw ingress, scaling and bounded diagnostics: [normalization and quarantine](docs/operations/quarantine.md).

Numeric ingestion, synced intake and outage recovery: [history mapping](docs/protocol/history-mapping.md).

Independent consumer smoke: [local slice](docs/tutorials/local-slice.md).
Service identities and segmented TLS profile: [identity and networking](docs/operations/identity-networking.md).

Read-only OT lab preparation and outstanding site approval: [pilot evidence](docs/pilot/integration-evidence.md).

Operational counters/limits: [diagnostics](docs/operations/diagnostics.md).
Isolated fault matrix and loss evidence: [recovery](docs/operations/recovery-evidence.md).

Cloud transfer with durable receipts and an isolated test receiver:
[transfer implementation](docs/integration/zeus-transfer.md) and
[cloud outage evidence](docs/operations/cloud-outage-evidence.md).

Reviewed reference configuration revisions and cold recovery:
[rollout](docs/operations/configuration-rollout.md) and
[backup/restore](docs/operations/backup-restore.md).

Current evaluation status, operator checklist and evidence map:
[MVP evaluation package](docs/releases/mvp-evaluation.md).
Pilot capacity qualification remains pending; the
[capacity workload](docs/operations/capacity-report.md) measures a reference slice.

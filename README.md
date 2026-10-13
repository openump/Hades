# OpenUMP — Hades

Hades is the industrial edge stack of OpenUMP (Open Unified Machine Protocol). It acquires read-only machine observations, normalizes them, publishes contextualized data over MQTT, and supports local numeric history and outbound cloud transfer.

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

## Contracts and capability checks

- [Namespace and identity](docs/protocol/namespace.md)
- [Telemetry contract](docs/protocol/telemetry.md) and [versioning](docs/protocol/versioning.md)
- [Metadata and producer status](docs/protocol/metadata-status.md)
- [Selected dependency inventory](docs/dependencies.md) and [native probes](examples/component-probes/README.md)

Run `./scripts/check-contracts` for the offline Go conformance CLI. [Conformance documentation](docs/protocol/conformance.md) describes fixture checks, review baselines, and CI. Probe results cover native loopback capabilities, not a deployed stack.

## Planning and contributions

Implementation tickets live in [Hermes](https://github.com/openump/Hermes/blob/main/plans/hades/PROJECT_PLAN.md). Reference their stable `Hades-` IDs in commits and implementation PRs. See [CONTRIBUTING.md](CONTRIBUTING.md) for verification and decision conventions.

## License status

The owner has not yet selected a project license. No open-source license grant is asserted by this repository until that decision is recorded and a `LICENSE` file is added. See [ADR-0001](docs/decisions/0001-license-policy.md). The intended open-source reference stack requires explicit review of each dependency's exact edition and license.

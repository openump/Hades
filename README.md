# OpenUMP — Hades

Hades is the industrial edge stack of OpenUMP (Open Unified Machine Protocol). It acquires read-only machine observations, normalizes them, publishes contextualized data over MQTT, and supports local numeric history and outbound cloud transfer.

This repository currently contains the [white paper](WHITEPAPER.md) and foundation work. A production deployment is not yet available.

## Development setup

Git and Python 3.11 or later are required for contract development. Runtime probe prerequisites are documented alongside each probe when introduced.

```sh
git clone https://github.com/openump/Hades.git
cd Hades
cp .env.example .env
git diff --check
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

The contract checks are foundation tests; the complete conformance CLI and CI are Hades-006. Probe results cover native loopback capabilities, not a deployed stack.

## Planning and contributions

Implementation tickets live in [Hermes](https://github.com/openump/Hermes/blob/main/plans/hades/PROJECT_PLAN.md). Reference their stable `Hades-` IDs in commits and implementation PRs. See [CONTRIBUTING.md](CONTRIBUTING.md) for verification and decision conventions.

## License status

The owner has not yet selected a project license. No open-source license grant is asserted by this repository until that decision is recorded and a `LICENSE` file is added. See [ADR-0001](docs/decisions/0001-license-policy.md). The intended open-source reference stack requires explicit review of each dependency's exact edition and license.

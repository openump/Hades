# ADR-0001: Project license and dependency policy

**Status:** Accepted (`Apache-2.0` project license; accepted dependency review process)

**Date:** 16 October 2026

## Context

Hades provides an open-source industrial edge stack. Following foundation and MVP evaluation (`Hades-001`, `Hades-023`), the project owner selected **Apache License 2.0 (`Apache-2.0`)** for Hades's original code, schemas, and documentation.

## Decision

License Hades's original code, schemas, and documentation under the **Apache License 2.0** (`LICENSE`), selected for its explicit patent grant and compatibility with permissive industrial and cloud ecosystems.

Accept dependencies only after recording exact release, edition, official source, license, maintenance evidence, architecture, and needed capability. Prefer OSI-approved permissive licenses; review copyleft and redistribution requirements explicitly. Reject source-available-only dependencies from the open-source baseline unless the owner records an exception and the product description is corrected.

## Alternatives and consequences

MIT was considered for its shorter text, but `Apache-2.0` provides explicit patent terms appropriate for industrial protocol and edge runtime software. Dependency license choices remain separate from Hades's own license; transitive and binary-distribution notices must be preserved when distributing prebuilt artifacts.

## Verification

Review README and CONTRIBUTING for consistent license status. Confirm credentials and runtime state are ignored while `.env.example` remains trackable. Verify the README setup in a clean checkout.

## Sources

- [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0)
- [OSI license list](https://opensource.org/licenses)

## Tracking

[Hades-001](https://github.com/openump/Hermes/issues/1)

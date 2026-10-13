# ADR-0001: Project license and dependency policy

**Status:** Proposed project license; accepted dependency review process

**Date:** 13 October 2026

## Context

Hades intends to provide an open-source industrial stack. The owner has not selected the license for Hades's original code, schemas, and documentation. Implementing repository conventions must not invent that grant.

## Decision

Leave the project license pending owner selection. Apache-2.0 is a candidate for its explicit patent terms; MIT is an alternative with simpler terms. Do not add a LICENSE file, SPDX project license declaration, or claim that the original repository is already licensed until the owner chooses. This pending decision is allowed by Hades-001 and is a release/distribution prerequisite, not a blocker for authoring the foundation.

Accept dependencies only after recording exact release, edition, official source, license, maintenance evidence, architecture, and needed capability. Prefer OSI-approved permissive licenses; review copyleft and redistribution requirements explicitly. Reject source-available-only dependencies from the open-source baseline unless the owner records an exception and the product description is corrected.

## Alternatives and consequences

Automatically selecting a license would close an unresolved owner decision without authorization. Recording the uncertainty preserves accurate licensing status. Dependency license choices do not license Hades itself; transitive and binary-distribution notices still require review before release.

## Verification

Review README and CONTRIBUTING for consistent license status. Confirm credentials and runtime state are ignored while `.env.example` remains trackable. Verify the README setup in a clean checkout.

## Sources

- [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0)
- [OSI license list](https://opensource.org/licenses)

## Tracking

[Hades-001](https://github.com/openump/Hermes/issues/1)

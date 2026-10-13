# Contributing to Hades

Use short-lived `codex/` branches for agent work. Keep each commit focused on one Hermes ticket and link its full cross-repository issue URL in the PR. Implementation belongs here; plans and status belong in Hermes.

Before committing, review the staged diff, run `git diff --check`, verify the checks relevant to the change, and inspect staged files for credentials or generated state. Record commands, versions, results, and known limits in the PR. Do not claim runtime behavior from documentation alone.

Run the setup commands in README.md on a clean checkout. Contract and component probe commands will be documented as those artifacts are introduced. There is no application build command yet.

## Architecture decisions

Store sequential records in `docs/decisions/NNNN-description.md`. Include status, date, context, decision, alternatives, consequences, verification, and sources. Accepted records capture implementation choices; proposed records identify decisions awaiting the owner. Supersede decisions with a new record rather than deleting their history.

Public contracts require examples, negative fixtures, a compatibility policy, and verification evidence. Distinguish structural JSON Schema checks from semantic checks that need metadata, time, or deployment context.

## Dependency policy

Record exact versions, provenance, license identifiers, distribution obligations, supported architecture, and capability evidence before adding a dependency. Prefer maintained OSI-approved permissive dependencies. Review copyleft obligations per usage and distribution mode. Source-available/commercial-only components are not part of the intended open-source baseline without an explicit owner exception. Never describe such an exception as open source.

Development, runtime, and optional external integrations have distinct inventories. Do not vendor dependency source or licenses indiscriminately; document the actual artifacts and obligations. Runtime upgrades require repeated capability probes and review of license and maintenance changes.

## Runtime implementation

Follow [ADR-0003](docs/decisions/0003-runtime-language.md): write custom deployed services and operational CLI tools in Go; prefer existing components and configuration before adding code. Python may remain in development tooling. Use shared schemas and fixtures to verify Go/Python contract agreement during transition. Pin the Go toolchain and dependencies when introduced, and measure resource limits on the target hardware.

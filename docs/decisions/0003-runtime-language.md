# ADR-0003: Go for custom Hades runtime services

**Status:** Accepted by the project owner

**Date:** 13 October 2026

## Context

Hades prioritizes a lean edge footprint, predictable operational behavior, and maintainable industrial integrations. Foundation tickets introduced Python contract tests and disposable capability probes; these do not select Python for deployed services. The owner agreed to Go for custom runtime code.

## Decision

Implement custom deployed Hades services and operational CLI tools in Go. This includes missing protocol/semantic validation, historian adapters, bounded durable transfer/replay, and site configuration tools when those tickets require custom code.

Reuse Mosquitto, Telegraf, Bento, and VictoriaMetrics as selected in ADR-0002. Prefer declarative configuration for capabilities they already provide; do not replace upstream drivers or stream processors merely to unify implementation languages. Add services only when a missing capability or a clear isolation boundary justifies them.

Keep Python for development tests, simulations, fixture verification, and capability probes. Production Hades installations must not require a Python interpreter or Python packages for custom application services. Existing Python checks remain development references during implementation; JSON Schemas, protocol specifications, and shared fixtures are the contract authority.

Compile schemas once per validator initialization where supported, bound payloads and queues, and avoid repeated parsing or unnecessary copies in the data path. A future Go validator must pass the shared positive/negative fixtures and boundary tests, including strict JSON parsing, timestamps, metadata matching, and freshness. Any differing behavior must be resolved explicitly rather than maintaining competing protocol definitions.

Select an exact supported Go toolchain and versioned libraries when the first Go implementation is introduced. Prefer pure Go dependencies where practical for Linux amd64/arm64 deployment; dependencies requiring cgo need a documented packaging rationale. No toolchain version or runtime dependency is added by this documentation decision.

## Alternatives

Python supports rapid development and useful test tooling, but introduces an interpreter/package environment on the edge if used for deployed services. Keep its development value without making it a runtime prerequisite.

Rust is a viable alternative for tighter memory or latency constraints, but introduces a second custom implementation language and additional development complexity. Reconsider for a specific component only if measured requirements cannot be met adequately with Go.

C/C++ remain appropriate inside existing upstream components and unavoidable native libraries. They are not the default for new Hades application services.

## Consequences and verification

Go provides compiled executables and concurrency support with a relatively small language/tooling surface. Its garbage collector still contributes memory use and latency; this decision does not assert hard real-time behavior, fixed RAM savings, or a benchmark result.

Measure the complete stack on target hardware: CPU, resident memory, throughput, processing latency, disk writes, queue bounds, and recovery under outage. Component count and buffering may dominate language-level savings. Deployment and capacity tickets must record these measurements before recommending production limits.

The Python foundation remains intact and no production service is claimed by this ADR. Hades-006 establishes conformance tooling; Hades-011, Hades-018, and Hades-020 apply this decision to custom adapters and CLI work. This supplements ADR-0002 rather than superseding its upstream component choices.

## Sources

- [Go language and engineering goals](https://go.dev/doc/faq)
- [Go garbage collector guide](https://go.dev/doc/gc-guide)
- [Telegraf upstream](https://github.com/influxdata/telegraf)
- [Bento configurable stream processing](https://warpstreamlabs.github.io/bento/docs/about/)

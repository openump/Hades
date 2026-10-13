# Hades-006/007 verification

**Date:** 13 October 2026

## Hades-006

Go 1.27.2 compiles all four local schemas with format assertions and disabled remote loading. `./scripts/check-contracts` passes 82 fixtures with explicit expected rejection categories and the unchanged reviewed baseline. Schema corruption and unregistered remote references fail tests. Drift tests reject schema edits, fixture edits, and additional unreviewed inventory files.

`go test -race ./...`, `go vet ./...`, `go mod verify`, and Linux amd64/arm64 builds with `CGO_ENABLED=0` passed locally. The pinned govulncheck 1.9.0 reports no vulnerabilities in this check. The Python development-reference suite passes 11 test methods, including the shared wire cases; it is not a deployed runtime dependency.

[Hosted CI for the Hades-006 commit](https://github.com/openump/Hades/actions/runs/38430087554) passed on Ubuntu. CI validates formatting, modules, Go/race/conformance checks, Python reference checks, cross-builds, and the vulnerability scan. Cross-compilation is not proof of a deployed Linux industrial stack.

## Hades-007

Reviewed the delivery ownership/state table against accepted, rejected, retried, duplicate, restart, disk-full, expiry, and conflicting-content walkthroughs. The initial profile identifies volatile gateway/normalizer/broker gaps, distinguishes durable adapter intake from destination commit, bounds all queues, and isolates cloud overflow from local consumption.

Cloud retirement requires a matching authenticated durable receipt or an explicitly recorded loss disposition; ambiguous outcomes retain the original record. Permanent errors suspend the original intake record instead of quietly deleting it into diagnostic retention. The Zeus interface remains a proposal with explicit joint decisions.

This ticket delivers specifications and an ADR, not implemented queues, endpoints, fault recovery, or measured outage guarantees. Hades-008/010/011/017/018/019/022 own their implementation/verification. The proposed budgets must be calibrated on named hardware and workloads.

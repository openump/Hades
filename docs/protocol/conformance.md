# Contract conformance

Run from a checkout with Go available:

```sh
./scripts/check-contracts
```

The command compiles all four Draft 2020-12 resources with format assertions, validates the registry, checks 82 named topic/payload/wire fixtures and their rejection categories, and verifies the reviewed hash baseline. It needs no broker, Python interpreter, or network access for validation. Initial Go toolchain/module downloads require network access unless already cached.

For all Go boundary, context, freshness, and drift tests, run `go test -race ./...`. The optional Python development reference uses the README commands and the same fixtures. CI runs both implementations plus formatting, vet, module verification, Linux amd64/arm64 builds without cgo, and a pinned reachable-vulnerability scan. CI actions use immutable commits and read-only repository permissions; no deployment or production secrets are involved.

## Reusable Go validator

`internal/contracts.New(schemaFS)` compiles trusted local schemas once. Reuse the resulting validator for payload checks. Schema references are resolved from registered local resources only; missing/remote resources fail. The current absolute-end pattern is translated to Go RE2's absolute `$` anchor without changing its meaning. Other unsupported regex constructs fail compilation and require explicit implementation review.

`Validate(kind, bytes)` returns a parsed object or a categorized error (`wire`, `schema`, or `context`). `NewRegistry` validates allocation identifiers and duplicate IDs/paths; its `ValidateTopic` returns `namespace` errors for invalid publications. `ValidateContext` and `Freshness` operate on already validated payloads, trusted time, and applicable metadata; callers must not pass arbitrary unvalidated maps. Structural payload validation alone does not prove authorization, context availability, or freshness.

Wire parsing enforces 65,536 bytes, valid UTF-8/Unicode scalars, duplicate-key rejection, finite binary64-representable numbers, and a nesting limit of 64. Nonzero numbers that underflow binary64 are rejected instead of fabricated zero readings. Zero representations are normalized before schema arithmetic to avoid pathological large-exponent allocation. Exact nonzero number text is retained for JSON Schema's numeric checks.

Validate one payload with:

```sh
go run ./cmd/hades-check -kind telemetry -file /absolute/path/to/payload.json
```

This validates encoding, schema, and metadata-local semantics when applicable; it does not supply the external metadata/registry needed to validate a publication end to end. Exit status is nonzero on failure and diagnostics identify the category and field/case. The CLI bounds file reads before parsing.

## Drift and compatibility guard

`tests/conformance/baseline.json` hashes the complete JSON inventory under `schemas/` and `examples/contracts/`. Changed, removed, or added files fail verification, including edits that flip a negative fixture to valid. This conservatively flags every schema change, including incompatible changes; it is not a general algorithm proving semantic compatibility.

After an explicit compatibility review, regenerate intentionally:

```sh
go run ./cmd/hades-check -refresh-baseline
```

Never refresh automatically in CI. Review the schema/fixture diff and baseline together, follow [versioning.md](versioning.md), and record breaking migrations. A checksum update can bless an incompatible change, so the reviewer must assess meaning; hashes do not replace that review. The first baseline records the existing initial profile plus rejection categories and wire fixtures added in Hades-006.

## Pinned tools and sources

Go 1.27.2; `github.com/santhosh-tekuri/jsonschema/v6` 6.0.3 (Apache-2.0); `golang.org/x/text` 0.43.0 (BSD-3-Clause); development-only govulncheck 1.9.0 (BSD-3-Clause). Versions and hashes are in go.mod/go.sum; the scanner is pinned in CI. No additional regex engine or cgo dependency is required.

- [JSON Schema compiler, assertions, and loader API](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6@v6.0.3)
- [Go toolchain selection](https://go.dev/doc/toolchain)
- [Go vulnerability checking](https://go.dev/security/vuln/)

The project license remains pending ADR-0001. Review dependency/transitive notices before distributing compiled artifacts.

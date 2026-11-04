# Contributing to Hades

Use short-lived `codex/` branches for agent work. Keep each commit focused on one Hermes ticket and link its full cross-repository issue URL in the PR. Implementation belongs here; plans and status belong in Hermes.

Before committing, review the staged diff, run `git diff --check`, verify the checks relevant to the change, and inspect staged files for credentials or generated state. Record commands, versions, results, and known limits in the PR. Do not claim runtime behavior from documentation alone.

Run the setup commands in README.md on a clean checkout. Use `./scripts/check-contracts`, `go test -race ./...`, `go vet ./...`, and `go build ./cmd/hades-check` for Go conformance work. Component probe commands are documented with their examples. A change to the inspector console or `hades top` must keep `scripts/screenshot-stack` green; see [visual testing](docs/operations/visual-testing.md) for reviewing and refreshing baselines.

## Architecture decisions

Store sequential records in `docs/decisions/NNNN-description.md`. Include status, date, context, decision, alternatives, consequences, verification, and sources. Accepted records capture implementation choices; proposed records identify decisions awaiting the owner. Supersede decisions with a new record rather than deleting their history.

Public contracts require examples, negative fixtures, a compatibility policy, and verification evidence. Distinguish structural JSON Schema checks from semantic checks that need metadata, time, or deployment context.

## Dependency policy

Record exact versions, provenance, license identifiers, distribution obligations, supported architecture, and capability evidence before adding a dependency. Prefer maintained OSI-approved permissive dependencies. Review copyleft obligations per usage and distribution mode. Source-available/commercial-only components are not part of the intended open-source baseline without an explicit owner exception. Never describe such an exception as open source.

Development, runtime, and optional external integrations have distinct inventories. Do not vendor dependency source or licenses indiscriminately; document the actual artifacts and obligations. Runtime upgrades require repeated capability probes and review of license and maintenance changes.

## Runtime implementation

Follow [ADR-0003](docs/decisions/0003-runtime-language.md): write custom deployed services and operational CLI tools in Go; prefer existing components and configuration before adding code. Python may remain in development tooling. Use shared schemas and fixtures to verify Go/Python contract agreement during transition. Pin the Go toolchain and dependencies when introduced, and measure resource limits on the target hardware.

## Releases

A release is built by `scripts/package-release`: reproducible `.deb`, `.rpm` and
tarballs of the `hades` multicall, generated third-party notices and SBOMs, a
`checksums.txt` signed with the Ed25519 release key, and an `install.sh` with that
key's public half pinned. `scripts/build-airgap-bundle` adds the offline bundle. See
[packaging](docs/operations/packaging.md) for every artifact and check.

```sh
HADES_VERSION=1.2.0 HADES_RELEASE_KEY=/secure/release.key scripts/package-release
HADES_VERSION=1.2.0 HADES_RELEASE_KEY=/secure/release.key HADES_RELEASE_PUB=release.pub \
  HADES_ARCH=amd64 scripts/build-airgap-bundle dist/
```

- **Release key custody.** The release key is generated once with
  `hades release keygen` by the release owner, kept offline and readable only by them;
  the build refuses a key file readable by group or others. Never commit it or put it in
  CI: the `packaging` job uses a throwaway key that it generates per run. Publish the
  public key through a channel operators already trust, because the offline installer
  verifies against a key the operator supplies, never the copy inside a bundle.
- **Reproducibility.** The same commit, version and `SOURCE_DATE_EPOCH` (the commit
  time by default) give byte-identical artifacts; CI builds twice and compares. Before
  publishing, rebuild and compare `checksums.txt` with the one you sign.
- **Before publishing.** The `packaging` CI job must pass on the release commit: clean
  `.deb` and `.rpm` installs, the unit under real systemd, `install.sh` tamper refusal
  and the air-gapped install. Verify the signature yourself with
  `openssl pkeyutl -verify -pubin -inkey release.pub -rawin -in checksums.txt -sigfile checksums.txt.sig`.
- **A new linked dependency** needs a license text in its module (or the notices
  build fails) and a reviewed license identifier in `internal/compliance` (or
  `TestEveryLinkedModuleOfHadesHasAReviewedLicense` fails); record it in
  `docs/dependencies.md` as the dependency policy requires.
- **Fleet rollout** of a release across sites is out of scope here (Zeus-003).

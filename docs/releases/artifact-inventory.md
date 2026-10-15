# Reference candidate artifact inventory

No signed release or published image digest is asserted. Record the reviewed
Git commit and hashes of actual built outputs for each evaluation. Runtime
credentials, private archives and site data stay outside Git.

| Artifact | Version/source pin | Delivery / qualification |
| --- | --- | --- |
| Hades Go CLIs | go.mod: Go 1.27.2; go.sum pins modules | `go build ./cmd/...`; CI cross-builds Linux amd64/arm64. No release binaries published. |
| Mosquitto | 2.1.2 source hash, APK pins and Alpine digest in deploy/broker/Dockerfile | Locally built image tag; Linux CI runtime tested. Record resulting local image ID. |
| VictoriaMetrics community | 1.154.0 OCI index in deploy/historian/Dockerfile | Linux reference runtime tested; storage-format migration excluded. |
| Bento Bloblang | 1.22.0 module in go.mod/go.sum | Embedded mapping engine in normalizer; full connector runtime not deployed. |
| Telegraf | 1.40.2 archive hash in CI | FC3 loopback lab only; Siemens pilot pending. |
| MQTT client | Eclipse Paho Go v0.23.0 | Embedded in Go services, module checksum verified. |
| Contracts | Four schemas/v1 JSON files and examples/contracts baseline | 82 fixtures; preserve baseline checksums. |
| Deployment/configuration | deploy/compose*.yaml, broker policy, approved metadata and mappings | `openump-site/1` manifest records actual file hashes and revision. |
| Cold backup | `hades-site` private tar with file digest index | Separate trusted SHA256 required, secrets excluded, no publication. |
| Capacity evidence | benchmarks/reference-*.json | Reference single-process transfer-intake scope; not pilot sizing. |

See [dependency inventory and notices](../dependencies.md). The project license
remains unselected (ADR-0001); dependency notices and transitive redistribution
review remain release gates. The dated dependency inventory's early capability
notes are historical; use linked implementation/CI evidence for current tests.

For each actual build record: source commit, `go version`, OS/architecture,
`go version -m <binary>`, `shasum -a 256 <binary>`, Docker image IDs/RepoDigests,
site manifest digest, test-run links and operator review reference. Image tags
alone are not immutable distribution identities.

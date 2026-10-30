# Reference candidate artifact inventory

No signed release or published image digest is asserted. Record the reviewed
Git commit and hashes of actual built outputs for each evaluation. Runtime
credentials, private archives, and site data stay outside Git.

## Component and dependency inventory

| Artifact | Version/source pin | Delivery / qualification |
| --- | --- | --- |
| Hades Go CLIs (15 binaries) | `go.mod`: Go 1.27.3; `go.sum` pins modules | `go build ./cmd/...`; CI cross-builds Linux amd64/arm64 (`hades-check`, `hades-simulator`, `hades-consumer`, `hades-normalizer`, `hades-history`, `hades-history-proxy`, `hades-gateway`, `hades-opcua-gateway`, `hades-transfer`, `hades-pki`, `hades-site`, `hades-service`, `hades-capacity`, `hades-actuator`, `hades-infer`). No release binaries published. |
| Mosquitto | 2.1.2 source hash, APK pins and Alpine digest in `deploy/broker/Dockerfile` | Locally built image tag; Linux CI runtime tested. Record resulting local image ID. |
| VictoriaMetrics community | 1.154.0 OCI index in `deploy/historian/Dockerfile` | Linux reference runtime tested; storage-format migration excluded. |
| Bento Bloblang | 1.22.0 module in `go.mod`/`go.sum` | Embedded mapping engine in normalizer (`internal/normalize`); full connector runtime not deployed. |
| OPC UA mock software pilot (`hades-opcua-mock`) | `scripts/hades-opcua-mock`; `deploy/opcua/requirements-pilot.txt` pins `asyncua==1.1.6`, `cryptography==44.0.2` | Local read-only OPC UA pilot server (`Basic256Sha256` `SignAndEncrypt`, user X.509 certificates, versioned `press-04/v1` NodeId map); verified with `hades-opcua-gateway` in `scripts/smoke-opcua-pilot` and `scripts/benchmark-stack --opcua-pilot`. |
| Telegraf | 1.40.2 archive hash in CI | FC3 Modbus loopback regression fixture (`scripts/smoke-ot-gateway`). |
| MQTT client | Eclipse Paho Go v0.23.0 | Embedded in Go services, module checksum verified. |
| Durable WAL storage engine | `internal/storage` (CRC32C-framed `segment-*.wal` + compact index) | Embedded in `hades-transfer` and `hades-history` (`Hades-026`); single append + `fsync` per mutation, segment compaction, and automatic migration from legacy v1 JSON directories. |
| Contracts (`schemas/v1`) | Seven `schemas/v1` JSON files and `examples/contracts` baseline | 94 conformance fixtures across `telemetry`, `asset-metadata`, `producer-status`, `transfer-receipt`, `command`, `command-receipt`, and `actuation-policy` (`Hades-002`, `Hades-032`). |
| Deployment/configuration | `deploy/compose*.yaml`, `deploy/systemd/*.service`, broker policy, approved metadata and mappings | `openump-site/1` manifest records actual file hashes and revision; `hades-service` enforces digest-bound native systemd supervision (`Hades-028`). |
| Cold backup | `hades-site` private tar with file digest index | Separate trusted SHA-256 required, secrets excluded, no publication (`Hades-021`). |
| Capacity and retention evidence | `benchmarks/reference-*.json`, `docs/operations/capacity-report.md`, `docs/operations/retention-sizing.md` | Single-process transfer-intake baselines plus repeatable whole-stack OPC UA pilot (`--opcua-pilot`) and multi-context (`single-asset`, `two-assets`, `four-assets`) steady/burst measurements and Tier 2 IPC retention/endurance bounds (`Hades-022`, `Hades-025`). |

## Go CLI binary manifest (`./cmd/...`)

| Binary | Package | Role |
| --- | --- | --- |
| `hades-check` | `./cmd/hades-check` | Offline contract, schema, topic, and baseline conformance verifier |
| `hades-simulator` | `./cmd/hades-simulator` | Synthetic telemetry, metadata, and producer-status publisher |
| `hades-consumer` | `./cmd/hades-consumer` | Validating MQTT consumer with duplicate and sequence tracking |
| `hades-normalizer` | `./cmd/hades-normalizer` | Raw ingress HTTP/Bloblang normalizer, bounded quarantine, and MQTT publisher |
| `hades-history` | `./cmd/hades-history` | WAL-backed MQTT-to-VictoriaMetrics numeric history adapter |
| `hades-history-proxy` | `./cmd/hades-history-proxy` | Scoped mTLS read/write proxy for VictoriaMetrics |
| `hades-gateway` | `./cmd/hades-gateway` | Read-only Modbus/Telegraf OT acquisition gateway |
| `hades-opcua-gateway` | `./cmd/hades-opcua-gateway` | Read-only OPC UA (`Basic256Sha256` `SignAndEncrypt`) pilot acquisition gateway |
| `hades-transfer` | `./cmd/hades-transfer` | WAL-backed store-and-forward cloud transfer service with durable Zeus receipts |
| `hades-pki` | `./cmd/hades-pki` | Disposable evaluation X.509 CA and service identity generator |
| `hades-site` | `./cmd/hades-site` | Site manifest validator, Compose rollout controller, and cold backup/restore CLI |
| `hades-service` | `./cmd/hades-service` | Digest-bound native systemd supervisor and pre-start verifier |
| `hades-capacity` | `./cmd/hades-capacity` | Reference and whole-stack capacity/outage/retention benchmark harness |

See [dependency inventory and notices](../dependencies.md). Hades original sources are licensed under `Apache-2.0` ([LICENSE](../../LICENSE), [ADR-0001](../decisions/0001-license-policy.md)); preserve upstream dependency notices and transitive SBOMs when redistributing binaries. The dated dependency inventory's early capability notes are historical; use linked implementation/CI evidence for current tests.

For each actual build record: source commit, `go version`, OS/architecture,
`go version -m <binary>`, `shasum -a 256 <binary>`, Docker image IDs/RepoDigests,
site manifest digest, test-run links, and operator review reference. Image tags
alone are not immutable distribution identities.

# Reference dependency inventory

Checked on 13 October 2026 for Hades-002. These are selected versions for the first implementation, not a production security certification. The project license remains pending in ADR-0001.

| Responsibility | Selected artifact | License | Architecture evidence | Required capability and maintenance evidence |
| --- | --- | --- | --- | --- |
| MQTT broker | Mosquitto 2.1.2, upstream source | EPL-2.0 OR BSD-3-Clause (EDL-1.0); use EDL distribution terms and preserve notices | Portable C source targeting Linux amd64/arm64; macOS arm64 source build tested. Linux builds remain untested. | MQTT QoS 1 pub/sub; current upstream download and v2.1.2 tag checked. |
| Normalization | Bento 1.22.0 | MIT | Upstream release archives for Linux amd64/arm64 and Darwin arm64; Darwin arm64 tested | Bloblang mapping; release published 9 October 2026. Future pipeline requires MQTT input/output and schema validation capability checks. |
| First driver candidate | Telegraf 1.40.2, standard distribution | MIT | Docker manifest for `telegraf:1.40.2-alpine` advertises Linux amd64/arm64; Darwin arm64 binary tested | `inputs.modbus`, `outputs.file`; release published 12 October 2026. Modbus TCP function 3 tested against loopback fixture only. |
| Numeric historian | VictoriaMetrics 1.154.0, community single-node | Apache-2.0 | Docker manifest for `victoriametrics/victoria-metrics:v1.154.0` advertises Linux amd64/arm64; Darwin arm64 binary tested | Prometheus import and instant query; release published 12 October 2026. No Enterprise artifact is selected. |

The Mosquitto 2.1.2 Docker Hub manifest was unavailable at inspection. Select a reproducible source-built image or reassess packaging in Hades-008; do not silently substitute an older broker or use `latest`. Bento's release archive architectures are confirmed; its container manifest is not yet verified. Hades-008 must record image digests and actual Linux runtime checks.

## Official sources

- [Mosquitto download](https://mosquitto.org/download/), [v2.1.2 source](https://github.com/eclipse-mosquitto/mosquitto/tree/v2.1.2), [license](https://github.com/eclipse-mosquitto/mosquitto/blob/v2.1.2/LICENSE.txt).
- [Bento 1.22.0 release](https://github.com/warpstreamlabs/bento/releases/tag/v1.22.0), [license](https://github.com/warpstreamlabs/bento/blob/v1.22.0/LICENSE), [mapping syntax](https://warpstreamlabs.github.io/bento/docs/guides/bloblang/about/).
- [Telegraf 1.40.2 release](https://github.com/influxdata/telegraf/releases/tag/v1.40.2), [license](https://github.com/influxdata/telegraf/blob/v1.40.2/LICENSE), [versioned Modbus configuration](https://github.com/influxdata/telegraf/blob/v1.40.2/plugins/inputs/modbus/README.md).
- [VictoriaMetrics 1.154.0 release](https://github.com/VictoriaMetrics/VictoriaMetrics/releases/tag/v1.154.0), [license](https://github.com/VictoriaMetrics/VictoriaMetrics/blob/v1.154.0/LICENSE), [single-node ingestion and query API](https://docs.victoriametrics.com/victoriametrics/single-server-victoriametrics/).

## Distribution review

Preserve the selected artifacts' copyright/license notices and inspect transitive notices/SBOMs before redistributing binaries. A top-level license is not an inventory of every linked library. Mosquitto's source build here links local OpenSSL and cJSON; packaging must document their versions and notices. No dependency binaries are committed or installed as persistent system services.

## Development tools

Python 3.11+ supports repository checks; Python 3.14.6 was used for the probes. JSON Schema development requirements are introduced with the contract checks. CMake 3.31.4, Apple Clang 17, OpenSSL 3.6.3, and cJSON 1.7.18 were used for the disposable native Mosquitto build. The probe runner uses only the Python standard library.

## Go conformance implementation (Hades-006)

Go 1.27.2 is pinned in go.mod. The runtime validator uses `github.com/santhosh-tekuri/jsonschema/v6` 6.0.3 (Apache-2.0) and `golang.org/x/text` 0.43.0 (BSD-3-Clause). The development-only vulnerability scanner is `golang.org/x/vuln` 1.9.0 (BSD-3-Clause). See [conformance documentation](protocol/conformance.md) for official API sources, checks, and architecture builds. Source dependencies are verified with go.sum and `go mod verify`; redistribution still requires notices.

## Development broker and examples (Hades-008/009)

The development broker builds Mosquitto 2.1.2 from the checked archive hash on an immutable Alpine 3.24 index. Direct APK dependencies are version-pinned in deploy/broker/Dockerfile; repository/transitive packages remain outside a hermetic archive. [Packaging details](tutorials/mqtt-development.md) distinguish the local build tag from a published image.

The Go examples use Eclipse Paho MQTT 5 `github.com/eclipse/paho.golang` 0.23.0 (EPL-2.0 OR BSD-3-Clause/EDL distribution terms), `github.com/gorilla/websocket` 1.5.3 (BSD-2-Clause), `golang.org/x/net` 0.61.0 (BSD-3-Clause), and x/text 0.43.0. The examples accept only loopback MQTT endpoints and do not use WebSocket transport, although the SDK includes that dependency. Preserve applicable notices before distributing binaries. See [Paho source/license](https://github.com/eclipse/paho.golang/tree/v0.23.0) and the Go module inventory.

# Local MQTT development baseline

Requires a supported Linux Docker Engine and Docker Compose v2 with `--wait`. macOS/Windows Docker environments may work but are not the Linux acceptance evidence. Run from the repository root:

```sh
docker compose -f deploy/compose.yaml config --quiet
docker compose -f deploy/compose.yaml up --build -d --wait
./scripts/smoke-broker
```

The smoke command intentionally restarts this development broker. It checks QoS 1 publication, graceful retained-state recovery, and a fresh connection/publication afterward. It does not test an existing client reconnecting automatically, crash/power-loss durability, cloud transfer, or a historian. Do not run it against a production broker.

A standard user-defined bridge provides access from host-side development tools through the published port; it is not an isolated internal-only network. The default host port is localhost-only `127.0.0.1:1883`; set `HADES_MQTT_PORT` in your environment to change the port. No management listener is exposed and the control API is disabled. Anonymous local clients can access only `openump/v1/acme/#` and disposable `hades/dev/#` probe topics. There are no committed passwords; production TLS, identities, and role authorization belong to Hades-013. Treat every process on the host/Compose network as a development peer with these shared permissions.

The container runs as UID/GID 1883 with a read-only root filesystem, no capabilities, bounded logs, and CPU/memory limits. Broker data lives in a named volume. Normal `docker compose -f deploy/compose.yaml down` preserves it. Never use `down -v` if that development state is needed.

## Packaging and persistence

The selected Mosquitto 2.1.2 official image tag was unavailable. The supplied multi-stage Dockerfile builds the exact upstream source archive with SHA-256 verification. Both stages pin the Alpine 3.24 multi-architecture index digest; direct APK build/runtime packages are pinned to their reviewed major.minor series (`~3.5`) and installed with `--upgrade`. The Alpine branch repository keeps only the newest revision of each package, so exact revision pins break on every security release; series pins take security revisions automatically, while a new upstream version still fails the build and needs a reviewed Dockerfile change. Upstream package repositories and transitive packages are not a hermetic build archive; APK verifies repository signatures.

The local built image is tagged `openump/hades-mosquitto:2.1.2-dev`; this does not imply a published registry image. Build from the checked-in Dockerfile, not a preexisting unrelated tag. Production packaging must record the resulting image digest, dependency notices/SBOM, and verified platform results. The included client tools use the same source release as the broker.

Persistent state is saved periodically and on graceful shutdown. A five-second autosave interval is not a per-message sync guarantee or a proven maximum crash-loss window. The smoke probe's retained messages are development-only; OpenUMP telemetry remains non-retained. Broker offline queues use the initial delivery-profile count/byte caps, but their full failure/overflow behavior remains Hades-017 verification.

## Operation

```sh
docker compose -f deploy/compose.yaml ps
docker compose -f deploy/compose.yaml logs broker
docker compose -f deploy/compose.yaml down
```

The hosted broker integration job creates an isolated Compose project, runs the smoke probe on Linux, and deletes only its disposable CI volume during cleanup. There is no production deployment in this ticket.

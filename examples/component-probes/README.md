# Native component capability probes

Use the exact versions in [the dependency inventory](../../docs/dependencies.md). Obtain upstream native binaries or build the versioned source for your host, verify published checksums where supplied, and supply their absolute paths. The runner checks reported versions before starting any services. Its output and archive hashes from the initial run are in [results.json](results.json).

```sh
python3 examples/component-probes/run.py \
  --mosquitto /absolute/path/to/mosquitto \
  --mosquitto-pub /absolute/path/to/mosquitto_pub \
  --mosquitto-sub /absolute/path/to/mosquitto_sub \
  --bento /absolute/path/to/bento \
  --telegraf /absolute/path/to/telegraf \
  --victoriametrics /absolute/path/to/victoria-metrics-prod
```

The probe starts disposable loopback listeners on available ports, uses temporary storage, checks each capability, and terminates services afterward. MQTT anonymous access and retained probe publication apply only to this private temporary broker; telemetry retention and deployed authorization follow the eventual OpenUMP/runtime profile.

Modbus is a minimal TCP fixture that accepts only unit 1, function 3, holding register 0, length 1 and returns 672. Telegraf scales this to 67.2. It does not simulate a complete PLC, validate real-device byte layouts, or contact shopfloor endpoints.

## Native Mosquitto source build

For the recorded macOS probe, download and extract the official 2.1.2 archive. With CMake, a C compiler, OpenSSL, and cJSON installed, use task-specific absolute directories:

```sh
cmake -S /absolute/path/to/mosquitto-2.1.2 \
  -B /absolute/path/to/build \
  -DWITH_TLS=ON -DOPENSSL_ROOT_DIR=/absolute/path/to/openssl \
  -DWITH_WEBSOCKETS=OFF -DWITH_DOCS=OFF -DWITH_PLUGINS=OFF -DWITH_TESTS=OFF \
  -DCMAKE_INSTALL_PREFIX=/absolute/path/to/install \
  -DCMAKE_INSTALL_RPATH=/absolute/path/to/install/lib
cmake --build /absolute/path/to/build -j 4
cmake --install /absolute/path/to/build
```

Source SHA-256 is recorded, but was not independently signature-verified. Bento and VictoriaMetrics archive hashes were matched to their published checksum files; Telegraf's hash records the downloaded artifact without an independent signature check. Production provenance and redistribution review belong to packaging.

## Verification scope

Successful results prove only pub/sub, mapping, simulated acquisition, and a numeric write/query on the recorded host. The historian probe uses `-inmemoryDataFlushInterval=1s` and `nocache=1` so a recently imported sample becomes observable within the polling deadline. It does not prove durable acknowledgement, crash recovery, performance, Linux support, container startup, or real-device compatibility.

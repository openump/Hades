# Independent-consumer local slice

Hades-012 demonstrates raw simulator fixtures → Go/Bento normalization → MQTT →
synced Go history adapter → VictoriaMetrics, with two independent consumers.
The deployed normalizer, primary consumer and history adapter are Go. The second
consumer is a development oracle: the Mosquitto 2.1.2 C client transports MQTT,
then Python's independent Draft 2020-12 validator and reference context/freshness
implementation interpret the public contract. It imports no Go parsing or
consumer code. Python is not a custom production-service dependency.

From a clean checkout, install the pinned Go toolchain, Docker Compose and the
Python development dependencies, then run:

```sh
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements-dev.txt
docker compose -f deploy/compose.yaml up --build -d --wait
HADES_PYTHON=.venv/bin/python ./scripts/smoke-stack
```

The command starts the pinned historian and Go services, feeds good, uncertain
and null/bad raw readings, tests historian outage and adapter restart, replays
an exact duplicate, and compares both consumers' asset, identity, value, unit,
source observation timestamp, quality, freshness and duplicate decisions.
Fixture timestamps are deliberately stale (>3 acquisition intervals); neither
consumer fabricates freshness or substitutes a zero for missing data. A final
unique reading ends both consumers only after duplicate handling is observed.
It verifies the known numeric history and quality states, and checks a conflicting
identity is quarantined without overwriting original intake. Bounded waits
account for VictoriaMetrics' default recent-query delay; API acceptance and query
visibility remain separate from disk durability.

The Python oracle accepts bounded C-client JSON lines, checks exact locally
approved metadata/context, rejects retained telemetry and conflicting IDs, and
retains a FIFO window of 4,096 identities. Both example consumers use volatile
FIFO deduplication; restart loses their window. The historian adapter has its
separate synced dedup boundary. Unknown context, malformed input or overflow
fails visibly. C-client diagnostic SUBACK output supplies transport readiness;
telemetry itself is always decoded from JSON output, not diagnostic strings.

The developer's broker/container remains running after this check. Shut it down
with `docker compose -f deploy/compose.yaml --profile history down`. Add `-v` only
when deliberately discarding development broker/history storage. The isolated
CI stack is disposable and removed automatically.

Native macOS verification uses the same Go binaries and contract oracle with
explicit `HADES_VM_BINARY`, `HADES_MQTT_PUB`, `HADES_MQTT_SUB`, `HADES_MQTT_PORT`
and `HADES_VM_PORT` overrides. Hosted Linux CI executes the Compose path.

Sources: [Mosquitto JSON output](https://mosquitto.org/man/mosquitto_sub-1.html),
[delivery profile](../protocol/delivery.md), [history mapping](../protocol/history-mapping.md).

# ADR-0002: Open-source reference components

**Status:** Accepted for foundation capability checks; production packaging pending Hades-008

**Date:** 13 October 2026

## Context

The single-site MVP needs one broker, declarative normalization, one read-only industrial driver, and numeric history. The white paper names candidates without finalizing dependencies. Component acceptance must follow ADR-0001 and remain broker-independent.

## Decision

Select Mosquitto 2.1.2, Bento 1.22.0, Telegraf 1.40.2 with Modbus TCP as the first driver candidate, and community single-node VictoriaMetrics 1.154.0. Exact licenses, architectures, release evidence, and official sources are recorded in [dependencies](../dependencies.md).

The initial data path will use Telegraf observations, Bento normalization, MQTT publication, and a separate numeric ingestion adapter. Native capability probes establish each required foundation feature independently; they do not implement the full stack or prove container readiness.

## Alternatives

Current EMQX editions use source-available BSL terms and are excluded from this open-source baseline; pinning an obsolete edition just to preserve old licensing is not the selected strategy. This removes reliance on proprietary broker-native cloud connectors. Cloud transfer remains an explicit adapter responsibility.

Apache PLC4X remains an option for later drivers, but a JVM-based gateway adds another runtime. It is unnecessary for the initial Modbus candidate. No claim is made that Telegraf supports every device/protocol; a real device is selected and checked in Hades-014/015.

A relational event/document database and Kafka are not required for the numeric-history capability. VictoriaMetrics receives numeric metrics through an adapter, not arbitrary MQTT JSON.

## Consequences

These maintained upstream releases pass native capability checks. Mosquitto's exact Docker tag is not available, so image packaging remains a tracked prerequisite. The upstream architectures are documented separately from tested host support. Hades-031 replaces the earlier deferral of Apache PLC4X. The gateway exposes three driver tiers behind the shared poll interface:

- **Tier 0, OPC UA:** an in-process client keeps one persistent SignAndEncrypt session. It performs batched multi-node reads, or creates a `MonitoredItem` subscription per signal when the acquisition map sets `subscribe`. A background loop keeps Publish requests outstanding so the subscription survives skipped polls, and polls read the latest snapshot. The client is `awcullen/opcua`, not the `gopcua` the ticket named, because it is already reviewed and shared with the mock server, and a second OPC UA stack would add risk without new capability. The subscription is recreated after a session reset.
- **Tier 1, PLC4X:** the in-process pure-Go PLC4X drivers (no JVM) handle `s7://`, `modbus-tcp://`, `eip://`, `logix://` (little-endian Logix EtherNet/IP), `ads://` and `bacnet-ip://`. All configured tags go in one read request, and PLC4X merges adjacent registers and data-block offsets into contiguous PDUs. Write, subscription and browse builders are not exposed. The tier is available as a library driver (`NewPLC4XConnector` with the acquisition map's tag addresses), built only with `-tags plc4x`. plc4go's public `drivers` package links all of its protocols (about 1.4M lines of generated code), which made the default race-enabled test run seven times slower. A dedicated CI job tests, cross-builds and vulnerability-scans the tagged build. No shipped binary opens a non-loopback PLC endpoint until site approval.

Hades-040 qualifies the PLC4X tier against disposable simulators for S7, EtherNet/IP (Logix), ADS and BACnet/IP. A `-tags plc4x` `hades-gateway` accepts `plc4go` lab contexts only with `-lab`, an acquisition map addressing every measurement, and a literal loopback endpoint ([PLC4X lab acquisition](../tutorials/plc4x-lab-acquisition.md)). Simulator tests prove typed reads, per-protocol request counts, session reuse, reconnect, and that no write service reaches the controller. Three upstream plc4go defects at the pinned commit are recorded there:
- an unsynchronized map write on every ADS connect, which blocks production ADS;
- no sign extension for negative BACnet integers;
- one CIP request per EtherNet/IP tag.
- **Tier 2, Telegraf:** one long-lived Telegraf 1.40.2 daemon streams JSON batches into the gateway over a Unix domain socket (`outputs.socket_writer`) inside a private 0700 directory. A supervisor restarts it with exponential backoff (250 ms doubling to 30 s) and reports each exit as a failed read. Nothing spawns `--once` per poll.

Tag addresses and deadbands live in a gateway-local acquisition map (`examples/gateway/`), not in the OpenUMP metadata contract. `hades-gateway` remains the loopback lab Modbus profile until site approval. TLS/authentication, persistent MQTT sessions, buffers, and crash recovery need their dedicated tickets.

Scan-cycle guardrails (Hades-031) apply to every tier: adjacent registers are coalesced into one read, each endpoint has a token bucket and a one-in-flight limit, and a circuit breaker reports `degraded` then `offline` through `producer_status`. After each backoff window it admits one probe and doubles the window if the probe fails. Polls the gateway skips to respect its own rate budget are reported as `scan-protection` and do not count as controller failures. Per-signal deadbands omit readings within `±ε` of the last reported value until `max_silence_interval` forces a heartbeat. Suppressed readings are never re-sent with a new timestamp. PLC4Go tag addresses must be configured explicitly for every measurement.

## Verification

[Probe runner](../../examples/component-probes/README.md) and [results](../../examples/component-probes/results.json) record successful MQTT QoS 1 publication, Bloblang scaling with provenance preserved, Modbus function 3 scaling, and numeric historian import/query. Docker's daemon was unavailable, so testing used loopback-only native binaries on macOS arm64. No OT device was contacted.

The initial TLS-disabled Mosquitto source build failed because upstream requires a random provider; the successful build enables OpenSSL. The initial instant query found no sample before the historian flush/cache boundary; the successful probe uses a one-second flush interval and uncached queries. These settings are for a capability probe, not production durability tuning.

## Tracking

[Hades-002](https://github.com/openump/Hermes/issues/2)

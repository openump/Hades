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

These maintained upstream releases pass native capability checks. Mosquitto's exact Docker tag is not available, so image packaging remains a tracked prerequisite. The upstream architectures are documented separately from tested host support. Hades now exposes three gateway-driver tiers behind the shared poll interface: a native OPC UA reader for the approved software pilot profile, a PLC4Go-style brownfield abstraction for contiguous multi-tag polling across selected controller schemes, and the supervised Telegraf Modbus sidecar fallback. The Telegraf Modbus path now runs as a long-lived child under `hades-gateway` instead of spawning a fresh `--once` process for every poll. TLS/authentication, persistent MQTT sessions, buffers, and crash recovery need their dedicated tickets.

## Verification

[Probe runner](../../examples/component-probes/README.md) and [results](../../examples/component-probes/results.json) record successful MQTT QoS 1 publication, Bloblang scaling with provenance preserved, Modbus function 3 scaling, and numeric historian import/query. Docker's daemon was unavailable, so testing used loopback-only native binaries on macOS arm64. No OT device was contacted.

The initial TLS-disabled Mosquitto source build failed because upstream requires a random provider; the successful build enables OpenSSL. The initial instant query found no sample before the historian flush/cache boundary; the successful probe uses a one-second flush interval and uncached queries. These settings are for a capability probe, not production durability tuning.

## Tracking

[Hades-002](https://github.com/openump/Hermes/issues/2)

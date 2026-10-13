# ADR-0004: Explicit acceptance boundaries and bounded queues

**Status:** Accepted Hades design; cloud receipt contract proposed for joint Zeus agreement

**Date:** 13 October 2026

## Context

QoS 1, a successful HTTP response, and a readable sample do not establish end-to-end durability. Edge resources are finite, and cloud outages must not block local acquisition. The selected upstream components have different buffering and persistence behavior that must be verified against their exact releases.

## Decision

Use QoS 1 with stable observation IDs and duplicate-tolerant consumers. Declare raw gateway/normalizer acceptance and local MQTT publication as best-effort transport stages in the initial profile. Do not label their acknowledgements durable. Introduce bounded durable intake queues at historian and cloud-transfer adapters, protecting observations only after their own verified sync boundary. Keep cloud queues independent of local history and acquisition.

Define maximum bytes and records for every queue, explicit expiry/overflow policy, and diagnostic counters. Prefer rejecting new work with observable loss over unbounded growth. HTTP ingress can backpressure its source; a full cloud queue must not backpressure the local producer indefinitely. Preserve original envelope, topic, and ID during retry/replay.

Retire a cloud record only after a validated authenticated receipt stating durable acceptance for the same identity and digest. A broker PUBACK is insufficient to retire the cloud queue. Historian API acceptance is also distinct from verified database disk commit; the historian adapter must describe the loss boundary when retiring its intake records.

## Alternatives and consequences

A fully durable journal at every acquisition and normalization hop could reduce loss windows but adds disk writes, persistence complexity, and additional acknowledgements. It is not claimed by this MVP design. An operator requiring every accepted sample to survive all crashes needs a separately reviewed stronger profile; the initial profile explicitly exposes its volatile gaps.

Dropping the oldest cloud observations silently would obscure outages and replay ordering. The selected default rejects new records at capacity and expires old records only under an explicit maximum-age policy, recording losses. Local services continue within their own limits.

MQTT persistent sessions support reconnect delivery, but cannot substitute for an application queue, destination receipt, or measured broker crash guarantees. No exactly-once claim is made.

## Verification and implementation gates

The [delivery specification](../protocol/delivery.md) contains ownership transitions, budgets, retry policy, and scenario walkthroughs. The [Zeus transfer proposal](../integration/zeus-transfer.md) defines receipt verification and unresolved integration decisions. Hades-008/010/011/017/018/019 must implement and verify their applicable parts before reporting durability or operating limits. No queue implementation is delivered by this ADR.

## Sources

- [MQTT 5 acknowledgement, session, and expiry semantics](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html)
- [Mosquitto configuration](https://mosquitto.org/man/mosquitto-conf-5.html)
- [VictoriaMetrics single-node ingestion and operation](https://docs.victoriametrics.com/victoriametrics/single-server-victoriametrics/)

## Tracking

[Hades-007](https://github.com/openump/Hermes/issues/7)

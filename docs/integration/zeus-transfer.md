# Hades → Zeus transfer and durable receipt proposal

**Status:** Proposed interface for joint Hades/Zeus agreement. Hades-018 uses a contract-compatible test receiver until the deployed Zeus endpoint is agreed. This document does not claim a cloud service exists.

## Transfer request

Propose HTTPS `POST /v1/ingest/observations` with the original UTF-8 OpenUMP JSON bytes as the request body. Authenticate the Hades installation using an agreed service identity mechanism. The receiver derives organization/site permissions from that identity, not arbitrary payload/header claims. TLS and credential provisioning/rotation details remain an integration decision.

Request metadata:

- `Content-Type: application/json`.
- `X-OpenUMP-Profile: openump-transfer/1`.
- `X-OpenUMP-Topic`: the original publication topic.
- `X-OpenUMP-Digest`: lowercase SHA-256 of `UTF8(topic) || 0x00 || original_payload_bytes`.

The separator is unambiguous because valid topics exclude NUL. Persist the topic and exact body before delivery; do not reserialize JSON or update ingestion/observation time on retry. Validate schema/topic/asset scope against the applicable contract and metadata, including historical mappings for replay. Topic and payload limits remain the Hades profile limits; bounded headers are required.

The idempotency key is `(authenticated enterprise, message_id)`. The receiver verifies the digest independently. Same key and digest returns the existing receipt; same key with a different topic/body digest is a 409 conflict. A receipt must not be issued before the receiver's declared durable transaction/stream commit boundary.

## Receipt

Example fields (illustrative identifiers):

```json
{
  "profile": "openump-transfer/1",
  "enterprise": "acme",
  "message_id": "019a1234-5678-7000-8000-000000000001",
  "payload_digest": "<64 lowercase hexadecimal SHA-256 characters>",
  "receipt_id": "019a1234-5678-7000-8000-000000000099",
  "state": "durable",
  "accepted_at": "2026-10-13T14:00:01.000Z"
}
```

Hades verifies authenticated endpoint identity, bounded response size (at most 4 KiB), exact profile, authorized enterprise, message ID, digest, receipt syntax, and `state: durable` before retiring a queued record. The placeholder above is not a valid receipt fixture. Missing, malformed, mismatched, or volatile receipts do not retire the record. Receipt parsing schemas and security tests belong to Hades-018 and the corresponding Zeus ingress ticket.

The receipt means durable cloud ingress acceptance only. It does not assert completion of analytics, historian writes, agent reasoning, or ERP/CMMS actions. The receiver owns downstream recovery after issuing it. `accepted_at` records first durable acceptance and remains unchanged for duplicate requests.

## Response and retry behavior

| Result | Source behavior |
| --- | --- |
| 201 with valid durable receipt | New acceptance: durably retire after receipt verification. |
| 200 with valid original durable receipt | Identical duplicate: safely retire using the existing receipt. |
| 202 or 2xx without valid durable receipt | Pending/unknown durability: retain and retry within the delivery policy. |
| Connection failure or request timeout | Outcome unknown: retain and resend immutable identity/topic/body. |
| 429 or 5xx | Retry with bounded backoff and valid retry hints. |
| 401/403 | Pause affected transfer, expose credential/scope failure, preserve queue until repaired or explicit expiry. |
| 400/413/422 | Permanent input/profile error: quarantine/escalate; never mark delivered. |
| 409 digest conflict | Preserve conflict evidence and escalate; never rewrite the stored observation. |

For the initial proposal, retain deduplication identities, digest, and receipt for seven days from first durable acceptance; Hades's default queue replay age is 72 hours. Manual redrive after that window requires receiver reconciliation and explicit authorization. Numeric-history and enterprise-action idempotency are separate consumer responsibilities.

## Required integration decisions

Hades and Zeus owners must agree before connecting deployed stacks:

1. Endpoint, supported profile/schema versions, authentication, credential rotation, and organization/site authorization.
2. Exact destination durable boundary, replication/failure guarantees, and evidence supporting receipt issuance.
3. Receipt schema, retry hints, deduplication retention, conflict handling, and manual redrive process.
4. Metadata transfer/archive ownership, missing-context retention limits, and historical location validation.
5. Event-time/late-data behavior, status/session reconciliation, and policies preventing replayed observations from triggering obsolete actions.
6. Queue/outage budgets, admitted throughput, service quotas, and recovery tests using named environments.

## Receiver walkthrough

Authenticate and bound request → validate envelope/topic and independently compute digest → look up enterprise/message identity → atomically persist original request, deduplication entry, and receipt at the declared durable boundary → return receipt. A crash before commit yields no durable receipt; retry may create the record. A crash after commit but before response yields the same receipt on retry. Conflicting content never replaces the original.

These transitions must be tested in Hades-018/019 and Zeus ingestion work. A transport connector or MQTT PUBACK alone does not implement this receipt protocol. Alternative MQTT/stream transports are acceptable only with an equally explicit, jointly verified acceptance boundary.

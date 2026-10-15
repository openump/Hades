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

## Hades-018 reference implementation

`cmd/hades-transfer` now implements this proposal against the Go test fixture at
`tests/fixtures/cloud-receiver`. This establishes an executable Hades contract;
it does not establish a deployed Zeus endpoint or its durability guarantees.
The fixture boundary is a synced local file and containing directory, with the
original topic/body, digest and stable receipt in one record. No replication,
cloud analytics, enterprise actions or production enrollment is implied.

Build and start with approved metadata and private credentials:

```sh
go build -o artifacts/hades-transfer ./cmd/hades-transfer
artifacts/hades-transfer -broker tls://localhost:8883 \
  -tls-ca /private/broker-ca.crt \
  -tls-cert /private/transfer-adapter.crt -tls-key /private/transfer-adapter.key \
  -receiver https://approved-receiver.example/v1/ingest/observations \
  -cloud-ca /private/cloud-ca.crt \
  -cloud-cert /private/cloud-transfer.crt -cloud-key /private/cloud-transfer.key \
  -config examples/site/metadata.json \
  -intake runtime/transfer-intake -quarantine runtime/transfer-quarantine
```

The example endpoint is a placeholder. Broker and cloud trust can be separate.
The receiver transport requires verified mTLS, a fixed HTTPS endpoint, a ten-second
request deadline, one active connection/request, bounded headers and receipts,
no environment proxy and no redirects. Retry uses full jitter up to 30 seconds;
integer `Retry-After` hints cap at 300 seconds. 401/403 pauses the worker while
intake and health output continue; repair credentials and restart. Permanent
400/409/413/422 responses suspend the original durable record. After reconciliation,
`-resume-id <message_id>` explicitly resumes one suspended record unchanged.

Only the approved asset's exact telemetry, metadata and status topics are
subscribed. The `transfer-adapter` broker role grants those subscriptions and
receive permissions, with no publication or management privilege. The fixture
binds the verified `transfer-adapter` certificate to its configured enterprise,
site and asset; topic/payload/header claims cannot broaden that grant.
Retained metadata/status can transfer; retained telemetry is rejected.

This first implementation pins one approved metadata revision for the lifetime
of its queue, including restart. Unknown revisions fail closed; changing the
snapshot while records depend on it can prevent startup. Preserve that snapshot
until drained/reconciled. Historical multi-revision metadata archival and deployed
Zeus authorization/retention remain joint integration work. The fixture never
automatically evicts receipts; at its 100,000-record / 256 MiB limit it returns
503. This exceeds the proposed seven-day dedup minimum only while its store is
retained and capacity is available; it is not a production retention service.

### Ownership, capacity and observability

The source defaults to 100,000 records / 256 MiB, including suspended records
and 4 KiB accounting overhead per record, plus a 128 MiB free-disk reserve.
`-queue-records` and `-queue-bytes` may lower these limits, never increase them.
One owner holds the private store lock; both admission and retirement sync to disk.
A lost receipt or crash before retirement causes exact replay, not new identity.
Completed records are deleted only after verifying a 200/201 durable receipt.
The remote stable receipt handles duplicate replay after deletion or restart.

When transfer capacity or disk reserve refuses **new** intake, the MQTT delivery
is explicitly acknowledged and dropped for cloud transfer only. `drops` counts
refused deliveries, which can include repeats, rather than unique source IDs.
A digest-only `transfer_drop` log provides correlation. Local broker/history
consumers have separate subscriptions and queues. Non-capacity storage failures
stop this adapter without ACK; they do not stop the local pipeline. Invalid
input/conflicts go to the bounded synced diagnostic store before ACK. Permanent
receiver errors retain the original suspended record in the transfer queue.

Replay is paused after 72 hours of local durable ownership, independently of
untrusted source clocks. The record remains suspended and occupies capacity;
there is **no automatic deletion or fabricated event-time expiry**. An operator
must reconcile with the receiver before `-resume-id` supplies a new replay budget.
This conservative interpretation preserves evidence beyond the ordinary replay
window. IDs, topic, bytes, observed/ingested times and digest never change.

A `transfer_stats` JSON event each second exposes pending/suspended counts, oldest
owned age, queue bytes/limits/free reserve, admitted/delivered/retry/receipt-error
counts, capacity drops, conflicts and authorization pause. Counters and logs are
process-lifetime evidence; durable pending/suspended ownership recovers from disk.
An external collector/source ledger is required for loss accounting across
restarts. This output reports transfer separately from local ingestion health.

The bounded file store currently keeps its index and payloads in memory. A full
256 MiB profile can need more RAM than the generic 256 MiB service template.
Before deployment, lower the queue budget or provision and measure a suitable
memory limit; the lab test uses four records. Do not claim the default fits the
pilot host before a host and load are selected. Route JSON stdout to a bounded
collector; `-log-file` optionally rotates diagnostic stderr using the shared
operations logger. No service is installed by this ticket.

### Reproducible checks and authoritative API references

```sh
go test -race ./internal/transfer ./tests/integration/bridge
go build -o artifacts/hades-test-receiver ./tests/fixtures/cloud-receiver/cmd
```

The fixture CLI binds literal loopback only and requires mTLS. A local
`-fault-file` can select `offline`, `receipt-loss` or `normal`; this mechanism
belongs exclusively to the test fixture. Its credentials are disposable output
from `hades-pki`; no keys or runtime data enter Git.

Implementation API behavior was checked against the official
[Go HTTP transport](https://pkg.go.dev/net/http#Transport),
[Go TLS configuration](https://pkg.go.dev/crypto/tls#Config) and
[Eclipse Paho client configuration](https://pkg.go.dev/github.com/eclipse/paho.golang/paho#ClientConfig)
documentation. Toolchain and dependency versions remain pinned in go.mod/go.sum.
Outage evidence and limits are recorded in
[cloud outage evidence](../operations/cloud-outage-evidence.md).

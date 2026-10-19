# Normalizer ingress acknowledgement (Hades-029)

HTTP `202` with `{"acceptance":"accepted_broker_ack"}` requires successful MQTT
QoS 1 PUBACK for that normalized observation. Validation, mapping and entry into
the in-memory publication queue do not acknowledge gateway ownership. This is
the ticket's synchronous broker-ack option: no new disk spool and no
`accepted_durable` claim.

The gateway retains original bytes, identity and timestamps through HTTP errors,
disconnects, `503` and ambiguous responses. Only the explicit broker-ack receipt
releases its copy. Updated gateways reject old `accepted_volatile` receipts. Roll
out reviewed gateway and normalizer revisions together; mixed versions fail
closed and can keep retrying until both are upgraded. Gateway event logs now
identify the broker-ack boundary.

## Bounded publication waits

A validated request gets a four-second queue-plus-publication budget. The single
FIFO worker publishes outside the health/counter lock, with both the request
deadline and supervisor cancellation applied. It does not sleep/retry indefinitely
inside an HTTP request. Broker rejection, connection failure and deadline expiry
return `503` with `Retry-After: 1`; the gateway owns jitter/backoff and replay.

Pending and active publication waits share `-queue-records` (1–4096) and 16 MiB,
charging payload bytes plus 4096 bytes bookkeeping per record. The existing
16-request HTTP-handler admission limit also applies. Expired entries are removed
without publication. Dropping an unacknowledged wait leaves retry ownership at
the gateway; it does not silently retire that observation.

Ingress can start and expose health with the broker offline. In the background,
the worker first attempts a bounded startup connection to publish retained
metadata for consumer readiness. If unavailable, connection is retried on the next
publication request, using the pinned MQTT client's reconnect behavior. No ingress record is persisted: `spool_records` and
`spool_bytes` are zero. Diagnostic quarantine remains independently bounded on disk.

## Health and evidence boundaries

`pending_records` and `pending_bytes` include active publication waits. `published`
counts successful broker publications. `broker_accepted` counts handlers that
observed success and attempted to return the new receipt; it cannot prove remote
HTTP receipt. `publish_failures` counts failed/expired worker attempts,
`publish_timeouts` counts handler deadline/cancellation outcomes, and
`capacity_refusals`/`refused_bytes` expose pressure. `ingress_acceptance` and
`publication_wait_limit_ms` describe the active boundary. Counters reset on restart.

A normalizer crash before PUBACK cannot produce successful HTTP acceptance. If
PUBACK happened but the HTTP reply was lost, the gateway may replay the same
observation: existing downstream immutable-ID deduplication remains necessary.
No exactly-once claim is made.

PUBACK is broker transport acceptance, not history/transfer synced intake or a
power-loss-safe broker snapshot. Broker persistence windows, subscriber session
expiry, queue capacity and explicit overflow loss remain separate boundaries.
The integration check demonstrates both consumers under its configured healthy
session/capacity profile; it does not promise losslessness under arbitrary
subscriber outage or broker crash. Gateway acquisition itself is still volatile:
killing the gateway before acknowledgement can lose its unsaved reading.

## Subscriber disconnect race

The pinned Paho v0.23.0 client resets its manual ACK tracker on disconnect. The
outage test exposed `ErrPacketNotFound` after durable intake had already synced.
History/transfer now keep their reconnect workers alive for that specific outcome;
any redelivery follows existing immutable deduplication. Other acknowledgement
or storage errors still fail visibly. No subscriber acknowledgement is moved
ahead of durable intake.

## Verification

Unit/race checks withhold publication and prove no early HTTP success; check
failure/deadline `503`, cancelled queue expiry and legacy-receipt rejection; keep
health/publication responsive during quarantine I/O; and SIGKILL ingress during
a pending HTTP request, proving no 2xx was delivered for the lost wait.

The standard `scripts/smoke-security` scenario stops the broker, requires offline
`503`, starts the real gateway retry function with immutable bytes, kills and
restarts ingress mid-outage, requires another offline `503`, restores the broker,
and compares every acknowledged ID and source/time/context in history intake and
the independently receipted transfer receiver. All endpoints and state are
isolated disposable fixtures; this is not deployed Zeus or a physical PLC test.

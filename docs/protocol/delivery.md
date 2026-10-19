# OpenUMP initial delivery and buffering profile

This is the Hades-007 design specification, not evidence of a deployed reliable pipeline. It applies to the single-site reference stack and implements [ADR-0004](../decisions/0004-delivery-durability.md). All capacities below are proposed starting defaults requiring hardware/workload validation in Hades-022, not measured limits.

## Identity and ordering

Allocate `message_id`, source boot/sequence context, and observation time before the first delivery attempt. The gateway or a thin acquisition adapter must provide this identity; a normalization mapping must not generate a new ID on every retry. Telegraf's raw metric format alone is not assumed to meet this requirement. Hades-009/010/015 must demonstrate the chosen identity adapter behavior.

Retries and replay keep the original message ID, topic, envelope, metadata revision, and exact payload bytes. A new acquisition creates a new ID even if its measured value is unchanged. A process restart does not reconstruct lost memory-only observations from the current machine value; polling afterward creates new observations. Sequence is not a global ordering guarantee.

Consumers deduplicate within authenticated enterprise scope by message ID and compare the immutable topic/payload digest. Same identity with conflicting content is quarantined, not overwritten. Each consumer owns its durable deduplication state and retention. MQTT duplicate flags and packet identifiers do not replace application identity.

## Acceptance boundaries and ownership

| Stage | Acceptance | Ownership and retirement | Crash/loss boundary |
| --- | --- | --- | --- |
| Gateway acquisition | Reading captured with identity in the gateway's bounded volatile queue | Gateway retains through unsuccessful normalizer attempts; retires only after explicit `accepted_broker_ack` receipt. | Unsaved readings are lost on gateway crash or overflow; device polling cannot recover them automatically. |
| Normalizer ingress | `202 Accepted` with `acceptance: accepted_broker_ack` only after successful QoS 1 PUBACK | Gateway retains through queue waits/publication failures; `503` on bounded publication timeout. | No successful pre-PUBACK admission. Broker/subscriber crash and queue-limit boundaries still apply; no durable spool is claimed. |
| Local broker | Successful MQTT 5 PUBACK at QoS 1 | Broker handles configured routing/session queues; downstream consumers establish their own acceptance. | PUBACK does not establish historian/cloud commit or per-message disk sync. Unpersisted broker/session state may be lost on crash. |
| Historian adapter | Original observation recorded in its bounded intake log and synced | Adapter owns retry until the selected historian write acceptance/commit boundary is met and recorded retirement is synced. | Intake survives only after verified sync. If database API acknowledgement precedes disk commit, retiring on that acknowledgement exposes a documented database-crash loss window. |
| Cloud-transfer adapter | Original observation recorded in its bounded transfer log and synced | Adapter retries until a verified durable Zeus receipt, then durably retires the record. | Before local sync it is unprotected; after sync, receipt loss causes replay, not intentional deletion. |

These boundaries are intentionally separate. A durable adapter intake does not repair observations lost before that intake. A generic HTTP 2xx or MQTT callback is not evidence of downstream durability. Broker persistence/session settings must be recorded and tested; autosave intervals alone do not prove a strict maximum loss interval.

For a local HTTP ingress implementation, use 400 for malformed JSON, 413 for oversized bodies, 422 for permanent contract/mapping errors, and 503 with a bounded retry hint for temporary admission failure. Return 202 only after successful broker PUBACK, as implemented by Hades-029; otherwise return 503 and retain gateway ownership. Invalid observations never enter normal telemetry topics; diagnostics go to bounded quarantine. Permanent errors must not be retried indefinitely.

The historian adapter must distinguish durable intake, HTTP/API acceptance, and database commit. Hades-011/017 must identify and test the exact retirement boundary for VictoriaMetrics 1.154.0. If retirement follows API acceptance without verified sync, label that stage best effort and report the crash-loss window. Do not infer durability from a query returning the sample. A stricter profile must retain records until a verified durable boundary is available.

## Queue placement and initial budgets

All record limits and byte limits apply simultaneously; the first reached limit governs admission. Account for serialized records, indexing, and persistence overhead, not just payload length. A record may contain at most the protocol payload limit plus bounded topic/digest bookkeeping.

| Queue or store | Initial maximum | Policy |
| --- | --- | --- |
| Gateway pending readings | 4,096 observations or 16 MiB, memory only | Reject newest at capacity; count dropped acquisitions and mark degraded. Retry retained observations until accepted or lost on process restart. |
| Normalizer pending publication | 4,096 observations or 16 MiB, memory only | Refuse new ingress with 503 at capacity; four-second bounded publication waits, gateway owns retry. No silent oldest-record eviction. |
| Broker offline delivery, per durable client | 4,096 queued observations or 16 MiB | Configure available broker limits and session expiry; document the selected version's actual drop/refusal behavior. Broker overflow is a visible data-loss condition, not a delivery guarantee. |
| Historian adapter intake | 100,000 records or 256 MiB on disk | Reject newest with a loss counter at capacity; retain admitted work through sink failures. No automatic age expiry before sink acceptance. |
| Cloud-transfer intake | 100,000 records or 256 MiB on disk | Reject newest with a loss counter at capacity; expire unreceipted records at 72 hours only with trustworthy time and a recorded expiry/loss event. |
| Invalid-message quarantine | 10,000 records or 64 MiB on disk | Evict oldest diagnostics at capacity with counters; this is explicitly lossy diagnostic retention. |
| Metadata revision archive | 10,000 snapshots or 64 MiB on disk | Never evict snapshots still referenced by queued records or retained history. Refuse context updates if no safe space remains and expose the unresolved context. |

If an upstream component exposes metric-count rather than observation/byte limits, document the mapping and add an admission wrapper where necessary; do not claim that a memory limit alone implements byte-based queue admission. Hades-010/016 validate actual bounds. Session queue caps require version-specific Mosquitto verification in Hades-008/017.

Maintain a shared free-space reserve of at least 128 MiB on the persistence filesystem, in addition to historian retention space and configured queue budgets. Validate the host's total budget before startup. Cloud limits must not consume the historian's reserved allocation. At reserve exhaustion refuse new disk intake, keep previously committed records intact, count losses, and alert through available health channels. Do not delete committed records merely to make a write succeed.

A full or unavailable cloud queue must not block local acquisition/history indefinitely. Use an independently bounded cloud worker and separate broker consumer identity/queue. If cloud intake cannot accept, explicitly count the lost cloud copy and complete transport handling according to the selected client/broker behavior. Transport acknowledgement in that case does not assert cloud durability. Local acquisition can still lose data if its own queues, host, or disk limits are exceeded.

Disk logs require crash-safe appends, verified sync before claiming durable intake, and crash-safe retirement/compaction. Batch sync is allowed only if acceptance waits for the batch sync; queued but unsynced records remain volatile. Corrupt/incomplete tails must not be silently treated as accepted records. Persistent deduplication state and metadata references must survive the same documented boundary.

## Queue state transitions

For durable adapter intake, `received → pending_sync → queued` establishes ownership only at the successful sync. `queued → in_flight → queued` covers temporary failure or uncertain remote outcome. Cloud `in_flight → receipted → retired` requires a verified receipt followed by durable retirement. A crash between receipt and retirement replays safely.

A permanent error changes the original queued record to `suspended`; it still occupies the bounded intake log. Quarantine contains diagnostic evidence, not a replacement that silently deletes the original. Other eligible records may continue without retrying that suspended record. Resume only after compatibility/credential repair that permits the identical record; changed content requires a distinct observation and explicit disposition of the old one.

Capacity rejection means the new record never reached durable ownership. Trusted age expiry or an explicitly audited operator discard is an intentional loss transition (`expired` or `discarded`), distinct from successful delivery. It must record the reason before reclaiming space where storage allows; if diagnostics cannot persist, expose the uncertainty through health/counters and do not claim receipt-based retirement.

## Retry policy

Retry temporary network failures, disconnects, timeouts, 429 responses, and 5xx responses with full jitter: delay uniformly sampled from zero to `min(30 seconds, 250 milliseconds × 2^attempt)`. Saturate the exponent rather than overflow it. Reset attempts only after successful acceptance. Respect a valid authenticated Retry-After value, capped at 300 seconds; ignore invalid hints and record diagnostics.

Allow at most one in-flight cloud request in the first implementation and a ten-second request deadline. A timeout after sending has an unknown remote outcome; retain and resend the same immutable record. Never generate a new ID to bypass an unknown outcome. Cloud retries continue within queue capacity and the 72-hour replay age limit. Use a trusted persisted enqueue time for age; if clock state is unreliable after restart, suspend age-based deletion and report it.

Permanent local schema/mapping errors are rejected/quarantined. Cloud 409 conflicts and 400/413/422 contract errors move to a bounded investigation state with diagnostic evidence; they are not marked delivered and are not automatically retried unchanged. Credentials/permission failures pause the affected transfer and require configuration repair, while local consumers continue. Loss caused by eventual capacity/expiry remains observable.

## Replay, deduplication, and metadata

Replay oldest admitted records first, preserving IDs and original observation time; this does not promise order across separate producers or queues. Keep late observations out of current-value replacement when a newer observation is already known. Historical replay uses the applicable metadata revision and historical topic mapping, not the current location alone.

Retain metadata snapshots for at least every queued observation's lifetime and every historian period requiring that context. Forward needed metadata alongside telemetry; Zeus may durably hold an observation with unresolved context but must not invent it. Its pending-context policy is a joint integration decision.

The proposed receiver deduplication window is seven days from first durable cloud acceptance, with stable receipts throughout that window. This exceeds the initial 72-hour local replay window, but manual redrive beyond the agreed window requires explicit reconciliation. History storage deduplication and numeric conflict policy remain Hades-011 requirements; cloud receipts do not establish exactly-once enterprise actions.

## Scenario walkthroughs

| Scenario | Required transition/result | Verification owner |
| --- | --- | --- |
| Valid local ingress | Gateway pending → normalizer bounded publication wait → successful PUBACK → explicit broker-ack receipt; adapter intakes establish later boundaries independently. | Hades-010/011 |
| Invalid observation | Permanent rejection/quarantine; no normal telemetry publication, no fabricated zero, bounded diagnostics. | Hades-010 |
| Normalizer queue full | 503, source retains/retries within its own bound; source overflow/restart can cause counted loss. | Hades-010/017 |
| Broker acknowledgement lost | Normalizer retries the same ID; consumers detect duplicates, and no fresh identity is allocated. | Hades-017 |
| Broker crashes after PUBACK | Publication may have left normalizer ownership but not reached adapter sync; report the measured loss window, not zero loss. | Hades-017 |
| Historian unavailable | Synced intake retries; full intake rejects new history copies visibly. Other local consumers remain independently bounded. | Hades-011/017 |
| Cloud disconnect | Transfer log retains synced records; local services continue within their budgets. | Hades-018/019 |
| Cloud accepts but receipt is lost | Source retains record; replay yields the original durable receipt for identical content. | Hades-018/019 |
| Cloud queue full or disk reserve reached | Reject newest cloud copy and count loss; do not hold the entire local stack hostage or delete acknowledged local history. | Hades-019 |
| Restart during append/retirement | Recover only committed intake/retirement states; uncertain retirement replays safely with the same ID. | Hades-017/019 |
| Conflicting duplicate | Quarantine/escalate conflict; never overwrite the existing observation or acknowledge delivery of changed content. | Hades-011/018 |
| Queue expiry or missing clock trust | Trusted age limit emits an explicit loss event; uncertain clock pauses expiry until reconciliation. | Hades-019 |

Measure losses by comparing produced, admitted, synced, retired, and received IDs. Report dropped records/bytes, reason, stage, oldest queued age, occupancy, retry count, and receipt failures. No claim of zero loss, guaranteed outage duration, or real-time deadlines is justified until failure and capacity tickets provide evidence. Sustainable replay also requires destination processing capacity above ongoing acquisition rate.

After a volatile-stage crash, the exact lost count may be unknowable without an external source record. Runtime diagnostics must report that uncertainty rather than fabricate counts. Failure tests use an independently recorded producer-ID set to measure the observed loss window. Durable intake assumes the chosen filesystem/storage honors sync; storage and restart behavior must be verified on the pilot host.

## Reference failure evidence

Hades-017's [recovery matrix](../operations/recovery-evidence.md) records volatile-stage loss, synced intake recovery, replay after unknown sink outcome, queue/quarantine capacity and a measured historian API-acknowledgement crash gap. Results qualify only the tested profile/host; pilot hardware and physical storage failure evidence remain pending.


Hades-018/019 reference behavior: the 72-hour limit suspends queued records for
receiver reconciliation instead of automatically deleting them. This preserves
unknown-clock and expired-window evidence while continuing to account occupied
capacity. Only verified receipts retire records. The implementation pins one
approved metadata snapshot; historical multi-revision deployment is not claimed.
See [cloud transfer](../integration/zeus-transfer.md) and the
[isolated outage matrix](../operations/cloud-outage-evidence.md).

Hades-029 supersedes the original volatile ingress transition. See [ingress acknowledgement](../operations/ingress-acknowledgement.md) for the implemented bounds and upgrade behavior.

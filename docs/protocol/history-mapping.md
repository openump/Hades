# Numeric history reference profile

Hades-011 extends raw normalization → MQTT with a Go adapter and community
VictoriaMetrics 1.154.0. This reference profile uses one locally approved asset
metadata snapshot, with up to the contract's 64 declared measurements. The
simulator tutorial remains one motor-temperature measurement. Changing context
while records are retained requires the future metadata archive; an incompatible
snapshot makes recovery fail instead of inventing context.

```sh
docker compose -f deploy/compose.yaml --profile history up --build -d --wait
go run ./cmd/hades-history
go run ./cmd/hades-normalizer
./scripts/smoke-history
```

Run commands from the repository root in separate terminals as needed. The
optional `history` Compose profile uses a pinned upstream OCI index, copies its
binary into the pinned Alpine base, and runs as UID/GID 10001 with a writable
named volume. The host HTTP port binds loopback, defaults to 8428, and can be
changed with `HADES_VM_PORT`. The adapter also requires a loopback address.
Production access controls and resource sizing remain Hades-016/022.

## Mapping

| Item | Rule |
| --- | --- |
| Value metric | `hades_observation_value` only for good-quality non-null numeric values |
| Quality metric | `hades_observation_quality`: good=0, uncertain=1, bad=2 for every declared reading |
| Labels | Stable `asset_id`, approved `measurement`, exact UCUM `unit`, approved `metadata_revision` |
| Unit conversion | None in the history adapter. Scaling/unit conversion happens in validated normalization. `Cel` and `K` remain separate series. Always select a unit before aggregating. |
| Timestamp | Source `observed_at`, truncated to integer Unix milliseconds. Never use adapter receipt time as measurement time. |
| Nonnumeric, null, uncertain, bad | Skip the value metric; retain original envelope and emit quality only. Increment `skipped_values`; never encode string/boolean/missing values as zero. |
| Identity | Asset-scoped message ID and SHA-256 of exact topic + NUL + original payload |
| Duplicate | Same ID/digest: acknowledge existing synced ownership, no second write |
| Conflict | Changed content under same ID, or different IDs sharing a configured series/millisecond: reject and sync diagnostic; never overwrite original ownership |

No message/boot/session IDs, reasons, raw values or ingestion times are metric
labels. Label values come from the one approved snapshot, limiting this profile
to at most 128 series per asset/revision. Sparse value history intentionally has
gaps; dashboards must inspect the quality series rather than assume good data.
Milliseconds are the supported precision; two readings in the same millisecond
must be reconciled upstream, not silently merged.

VictoriaMetrics uses `/api/v1/import/prometheus` with explicit millisecond
exposition timestamps. `-dedup.minScrapeInterval=1ms` deduplicates exact sample
replays after uncertain retirement. That setting does not replace application
identity/conflict detection. Query `/api/v1/query_range` with the approved asset,
measurement, unit and revision selector; `/api/v1/export` verifies actual stored
sample timestamps rather than query evaluation timestamps.

## Ownership, limits and recovery

A stable MQTT client ID derives from the telemetry topic; only one adapter may
own that subscription. MQTT 5 session expiry is 24 hours and PUBACK is manual:
original topic/body, digest and derived sample bytes are atomically written,
file-synced and directory-synced before acknowledgement. Receive Maximum is 20;
packet size is bounded at 70,000 bytes. A storage/capacity failure leaves the
packet unacknowledged and exits with an error. Broker session expiry/overflow
still bound delivery before intake. Local fsync can delay receive processing;
this profile makes no real-time deadline claim.

The single-owner intake/dedup store is private mode 0700/0600, limited to 100,000
records / 256 MiB, whichever comes first, with serialized overhead plus 4 KiB
charged per file and a 128 MiB filesystem free-space reserve. A stored record is
limited to 100 KiB including base64 and derived lines; oversized serialized
records are refused visibly. Completed dedup records share these limits and stay
for seven days after API acceptance. At capacity, reject newest; never evict
pending/suspended ownership or shorten dedup silently. This limits sustainable
intake rate as well as outage duration; no throughput claim is made. Expired
completed records are cleaned on admission at most once per minute. Pilot
sizing, filesystem crash drills and workload measurements remain Hades-017/022.

A single worker retries unchanged sample bytes with full jitter (250 ms
exponential base, 30 s ceiling), a 10 s HTTP deadline, no proxy and no redirects.
408/429/5xx and network/unknown outcomes retain pending ownership. 400/401/403
and other permanent 4xx suspend the original record, with a diagnostic copy.
Other pending records may proceed. After correcting the sink, restart with
`-resume-id <original-message-uuid>` to retry that immutable record. Original
payload, identity, topic and timestamp do not change.

Reference historian retention is one day. Before every send, observations older
than 24 hours or more than one minute in the future are suspended and retained,
with diagnostics, instead of relying on silent historian discard. These checks
require the adapter host clock to be correct. Longer outages therefore require
operator reconciliation with a historian configured for longer retention and a
reviewed adapter profile; changing the value/timestamp to fit is forbidden.
The reference does not automatically delete suspended data.

**Retirement boundary: best-effort API acceptance.** HTTP 200/204 marks a synced
`api_accepted` retirement, retained for dedup. It is not proof of VictoriaMetrics
disk commit or even a per-sample durable receipt. The one-second in-memory flush
setting is for development visibility, not a proven upper crash-loss bound.
Database crash after API acceptance may lose samples; a successful query is not
evidence of disk durability. Hades-017 must measure this window. There is no
zero-loss/exactly-once claim. Retained completed envelopes can support explicit
reconciliation, but automatic replay after database loss is not implemented.

The CLI emits ready and periodic JSON statistics for owned/pending/suspended
records, serialized bytes, admissions, acceptance, duplicates, conflicts,
skipped values, retries and diagnostic occupancy/eviction. Runtime counters
reset on restart; records and ownership states persist. No full industrial
payload is written to normal logs.

## Verification

`go test -race ./internal/history ./internal/storage` checks sync/reopen,
exclusion/capacity/corruption, retry, restart dedup, conflicting identities and
sample collisions, nonnumeric/null treatment, permanent suspension/resume,
HTTP status classification and out-of-retention ownership.

`scripts/smoke-history` runs the actual normalizer and adapter, stops the
historian, admits three known observations, restarts the adapter with pending
synced intake, restores the historian, replays duplicate/conflicting payloads,
and compares exact exported values/timestamps, quality samples and a range query.
It passed locally with native Mosquitto 2.1.2 and VictoriaMetrics 1.154.0 on macOS
arm64; hosted Linux CI repeats using the pinned Compose images. The test waits
for query visibility separately from API acceptance.

Sources: [VictoriaMetrics import/query API](https://docs.victoriametrics.com/victoriametrics/url-examples/),
[deduplication and retention](https://docs.victoriametrics.com/victoriametrics/single-server-victoriametrics/),
[pinned release](https://github.com/VictoriaMetrics/VictoriaMetrics/releases/tag/v1.154.0),
[Paho manual acknowledgement](https://pkg.go.dev/github.com/eclipse/paho.golang/paho#Client.Ack).

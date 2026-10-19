# Hades-028: concurrent normalization and bounded drains

The normalizer parses, maps and validates outside its publication queue lock.
Quarantine has a separate writer lock and a bounded FIFO list. Startup sorts the
existing diagnostic keys once to rebuild that index; eviction thereafter removes
its head without sorting the store. Health reads cached diagnostic counts.
Diagnostic disk writes cannot hold the publication lock. The existing 16-request
admission limit still returns 503 under saturation. Hades-029 now requires
MQTT PUBACK before `accepted_broker_ack`; see [ingress acknowledgement](ingress-acknowledgement.md).

## Historian batching

`hades-history -batch-records 64 -batch-bytes 1048576` sets the defaults.
Records may be 1–256; bytes may be 65536–1048576. Both bounds apply to each
Prometheus import body. An individual mapped record exceeding the configured
byte limit produces an explicit drain error and retains ownership.

One drain call sends at most one request, ordered by durable admission time and
identity for ties. An indexed heap selects only the bounded batch, avoiding a
full-backlog sort on every call; exact-ID removal also handles an older suspended
record resumed while a request is in flight. Records admitted during a request join a later batch. Expired
records suspend individually without contaminating a valid batch. Permanent HTTP
errors suspend the affected batch and preserve each observation for reconciliation.

After API acceptance, each record's terminal state is synced separately. This
is not an atomic multi-record storage transaction. A failure halfway through local
retirement leaves all unretired records recoverable after restart. The successful
prefix stays retired; only the remaining ownership replays.

## Retry scheduling and delivery boundary

History and transfer retain their full-jitter exponential backoff (up to 30 s).
A transient failure records `retry_at` and returns; later polling calls before
that deadline perform no downstream request. Transfer also respects Retry-After.
The history worker drains ready batches immediately; idle/backoff polling is
bounded to 100 ms. The transfer worker polls every 100 ms. A network attempt itself still uses the sink's
existing timeout; the change removes retry sleeps, not synchronous HTTP I/O.
Backoff state is process-local and restarts with the worker.

FIFO is preserved within each configured asset stream: a failed head batch or
transfer record delays its successors. Local history and cloud transfer remain
independent workers. Transfer retains one immutable observation per request and
requires its existing verified durable receipt; there is no new cloud batch API.
A transfer drain processes at most 256 successful/suspended records per call.

Historian delivery is **at least once**. If the server accepts some or all samples
but its reply is lost, Hades must replay unchanged samples. Exact-once remote
writes cannot be promised. Deterministic series/timestamp keys and the reference
VictoriaMetrics dedup configuration provide the existing reconciliation behavior.
`write_count` counts HTTP attempts, while `api_accepted` counts retired observations.
Health statistics expose batch limits and the next retry deadline.

## Verification

`go test -race ./...` covers concurrent mapping, health and publisher progress
while eight malformed requests wait on the diagnostic writer, retained batch
ownership after an ambiguous response, unchanged FIFO replay and restart after a
failure during the second local retirement of a three-record accepted batch.

Run the controlled backlog comparison with:

```sh
go test ./internal/history -run '^$' -bench BenchmarkBacklog10000 -benchtime=1x -count=1
```

It compares one-record requests (the previous request shape) with 64-record
requests over 10,000 synced observations, including real per-record retirement
syncs and a simulated 1 ms downstream latency per request. Intake setup is outside
the measured window. This is a controlled algorithm comparison, not sustainable
site capacity or a deployed historian throughput measurement.

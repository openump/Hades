> Scope update — 2026-10-15: the active pilot is now an OPC UA mock server.
> The Modbus evidence below is historical lab regression evidence; OPC UA
> server/client integration, node mapping and acceptance have not run yet.

# Read-only gateway integration: lab evidence only

**Status:** lab implementation ready; real pilot selection/authorization and
real-device comparison pending. Hades-014 and Hades-015 remain open.

The Go `hades-gateway` wrapper uses the selected Telegraf 1.40.2 Modbus input.
It generates its own private, fixed read-only configuration rather than accepting
an arbitrary Telegraf config. `-lab` is required and source URLs must contain a
literal loopback IP. There is no approved real-endpoint mode. A loopback tunnel
to equipment is not covered by lab permission.

The profile reads only unit 1, holding register 0, function 3, one UINT16 in AB
byte order. Telegraf requires an explicit scale even for raw values: scale=1.0
keeps raw 672, and the existing Bento mapping converts it once to 67.2 `Cel`.
Metadata revision 2 in `examples/site/modbus-lab.metadata.json` identifies the
fixture and `modbus-tcp` source. The existing normalizer/consumer can load this
validated context without requiring the simulator protocol. No schema changes
or conformance-baseline refresh are needed.

## Runtime behavior

One supervised Telegraf 1.40.2 sidecar stays alive for the whole acquisition
session and streams one reviewed JSON metric batch per configured interval to
the Go wrapper over its standard output. This removes per-poll child-process
creation and preserves one PLC-side session instead of reopening the endpoint on
every tick. The wrapper still checks exact driver version, caps stderr at 4 KiB,
accepts exactly one configured metric/field batch, and rejects ambiguous
values/times. The generated config permits no writes, discovery, other plugins
or busy retries. Device requests time out at 200 ms, gateway reads are capped at
2 seconds, and per-endpoint scan protection enforces a token budget, max-1
in-flight request, and exponential backoff on repeated timeout/error conditions.
A timeout, exception, missing metric or driver failure creates null/bad quality
with a bounded reason and emits `producer_status` degradation/offline events;
the gateway never fabricates zeroes or silently reuses stale success values as
fresh reads.

Successful observation time is the Telegraf gateway metric timestamp at
millisecond resolution. Failures use gateway UTC completion time; neither is a
device-origin timestamp. Ingestion time is envelope creation time, with
`clock_origin=gateway` and uncertainty=null until an operator measures clock
trust. Consumer freshness is therefore unknown, not falsely fresh.

IDs, boot/sequence, source, metadata revision and both timestamps are assigned
once before delivery. A single pending raw envelope owns the next ingress attempt;
new polling pauses under ingress backpressure. Transient errors and ambiguous
202 receipt bodies retry immutable bytes with full jitter and bounded numeric
Retry-After hints. Only an explicit `202 accepted_volatile` receipt retires this
volatile gateway copy. Permanent rejection stops visibly. Restart can lose the
one unsaved observation; no durable/zero-loss acquisition claim is made.

Ingress must be HTTPS with explicit per-role CA/certificate/key files, server
verification and no redirects/proxy. The gateway uses the Hades-013
`gateway-press-04` ingress role, while normalizer and history retain their distinct
MQTT/historian identities. Modbus itself remains unauthenticated on the isolated
lab source; a real site needs reviewed read-only privileges and OT network rules.

## Repeatable verification

Use the pinned Telegraf binary for your OS/architecture:

```sh
HADES_TELEGRAF_BINARY=/absolute/path/to/telegraf ./scripts/smoke-ot-gateway
```

The test creates disposable TLS identities/containers and a local Modbus fixture.
It checks each wire request: unit=1, FC=3, address=0, count=1. Four scheduled
responses are successful 672, timeout, illegal-register exception and recovered
672. It compares raw values `[672,null,null,672]`, normalized values
`[67.2,null,null,67.2]`, unique IDs, unchanged source times and unknown freshness.
Only the two good numeric samples reach value history with the same timestamps;
all four observations remain in the synced adapter's recorded acceptance state.
The same check also verifies authorization and issuer rotation/reconnection.

`go test -race ./internal/gateway` checks remote-source refusal, fixed read-only
configuration, strict metric parsing, no fabricated values/clock trust,
immutable retry and ambiguous receipt recovery. Hosted Linux CI obtains Telegraf
1.40.2 from its official release URL and checks the published SHA256 before the
container test. Native macOS arm64 passed using the same pinned upstream driver,
Mosquitto/plugin, VictoriaMetrics and Go mTLS services.

Linux amd64 archive SHA256:
`fcdc579a09227fa3e2e3e208d3cf75cfd03d42ae141a4b23b18659a94428bfa3`.
Linux arm64 archive SHA256:
`8794daf6ce0bd49fa42c3f2d1515608ed14689e0efbc7b3391bf39ed7d112dcc`.
These are published release hashes, not an independent signature attestation.

## Evidence still required for completion

Supply and approve the [site profile](site-profile.md), then compare the approved
real endpoint's readings against operator-verified references under the exact
firmware/manual revision. Review limits, signing/trust, rollback and clock
uncertainty before enabling any real-source mode. The generic lab register
cannot substitute for a manufacturer-specific signal map. There is no real-device
connection or operator sign-off in this evidence.

Sources: [Telegraf 1.40.2 release/hashes](https://github.com/influxdata/telegraf/releases/tag/v1.40.2),
[Modbus input](https://github.com/influxdata/telegraf/blob/v1.40.2/plugins/inputs/modbus/README.md),
[JSON timestamp/metric format](https://github.com/influxdata/telegraf/blob/v1.40.2/plugins/serializers/json/README.md),
[site approval checklist](read-only-checklist.md).

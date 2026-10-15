# OPC UA mock software pilot v1

This profile implements Hades-014/015 on local software resources. It replaces
the physical Siemens pilot direction. It does not establish PLC compatibility,
production capacity, operator acceptance or deployed Zeus processing.

## Pinned implementation and security

Server and client use MIT-licensed `github.com/awcullen/opcua v1.4.0`, upstream
commit `3cdbbee6cc600b55a964f816e4b9ac1481ef7a82`; go.sum pins module content.
The client is unchanged upstream code. Custom deployed acquisition is Go.

Upstream server v1.4.0 binds all interfaces despite an endpoint host and advertises
older/sign-only policies. `scripts/opcua-mock-tool` verifies modules and the exact
SHA256 of upstream server.go, copies the SDK into a disposable build directory,
and applies a small source patch: bind the explicit endpoint host, advertise only
Aes256_Sha256_RsaPss/SignAndEncrypt, and add a compile-time patch marker. A temporary
modfile selects this copy; the module cache and normal go.mod remain unchanged.
The mock's build tag and marker deliberately prevent ordinary unpatched builds.
This patch is fixture-only, not a maintained production SDK fork. Re-review it
before updating the dependency. The fixture test checks endpoint policies and
non-loopback refusal on the actual host interfaces.

Endpoint default: `opc.tcp://127.0.0.1:4840`. Both commands reject hostnames and
non-loopback source endpoints. Application URIs are `urn:openump:opcua:mock-server`
and `urn:openump:opcua:read-only-gateway`. `init` creates RSA-2048 application
identities with 24-hour validity, private files and URI/loopback SANs. Each side
trusts the exact peer leaf; no trust-all or certificate-verification bypass is
used. X509 user identity must match the trusted client certificate and URI.
Anonymous sessions, writes and restricted nodes are denied. A separate outsider
certificate is a negative-test identity, never a trusted peer. Public discovery
is part of OPC UA negotiation; it does not authorize reading the pilot nodes.

## Fixed address space and mapping

Namespace URI: `urn:openump:pilot:mock:v1`. The gateway reads NamespaceArray on
every connection and resolves the current index; it never assumes ns=2.
NodeIds are `ns=<resolved>;s=<name>`:

| Node name | UA type | Measurement | Unit/range | Reference |
| --- | --- | --- | --- | --- |
| Temperature | Double | motor_temperature | Cel, -40..150 | 67.2 |
| CycleCounter | UInt32 | cycle_counter | 1, 0..4294967295 | increments for each actual counter read |
| MachineState | UInt16 | machine_state | 1, 0..2 | 1 (0 stopped, 1 running, 2 fault) |

No browse-driven node acquisition or writes exist in the gateway. Reads request
only these three Value attributes, MaxAge=0 and both timestamps. Types are checked
exactly; no scaling occurs because the source supplies canonical units. Wrong
types/ranges become null/bad. Status severity maps Good to good, Uncertain to
uncertain with its value, and Bad to null/bad with a bounded reason. Missing nodes
return BadNodeIdUnknown; transport/deadline failure produces gateway-origin
BadCommunicationError, not a claimed server response.

One envelope per node preserves distinct node times. Source timestamp wins,
then server timestamp, then gateway UTC completion. Envelope time is millisecond
precision required by v1; extensions retain exact decoded upstream timestamps,
StatusCode, namespace URI/index, node name, timestamp/status origin, and the UA
picosecond fields (counts of 10 ps intervals). Missing timestamps remain null in
extensions. Clock uncertainty remains null, including on this same-host fixture;
consumers must not infer clock qualification. Source protocol is `opc-ua`, gateway
`opcua-mock-01`, metadata revision 3 in `examples/site/opcua-mock.metadata.json`.
The normalizer's opt-in `opcua-mock` profile validates this exact context and one
approved numeric signal per raw envelope, preserving extensions without changing
shared schemas. Existing Modbus and simulator profiles remain separate.

## Bounds and ownership

Polling is serialized, with at least one second after read completion before the
next poll, no catch-up burst, 1..1000 polls and an overall 15-minute deadline.
Connection attempt budget is two seconds; read budget is 300 ms (SDK service hint
500 ms). Failed reads abort the session; the next scheduled poll reconnects and
resolves namespaces again. There is no reconnect loop outside the poll schedule.
A poll may retain up to three volatile observations while submitting one at a
time. Original bytes/IDs/timestamps are reused across HTTP retry. Polling pauses
under ingress backpressure. `accepted_volatile` is not durable acquisition;
process restart may lose the current poll. Periodic bounded-supervisor diagnostics
report polls, transport failures, bad readings, admission and ingress retries.

The mock caps sessions at four and workers at four, restricts node access to an
Observer role, has no write/call permission, retains at most two 5 MiB ledger
files and runs at most 15 minutes. Counter reset on server restart is deliberate;
its range is bounded by the short fixture lifetime. Run only against disposable
local identities and state. Use the existing supervisor for private bounded logs.

## Reproduce

```sh
./scripts/opcua-mock-tool test
./scripts/smoke-opcua-pilot
```

The integration command uses the pinned secure Compose stack by default, issues
private temporary identities, runs actual source→gateway→mTLS normalizer→MQTT→
history plus Go and C/Python consumers, and compares durable receiver bytes.
Install requirements-dev.txt in the selected Python environment. Native mode
uses the same HADES_MOSQUITTO_BINARY/HADES_DYNSEC_PLUGIN/HADES_MQTT_PUB/
HADES_MQTT_SUB/HADES_VM_BINARY/HADES_PYTHON variables as smoke-security.
`HADES_TEST_SYSTEMD=1` installs finite gateway units on disposable Linux with sudo.

The matrix covers good, uncertain, bad, missing-node status, delayed read timeout,
server down, restarted server with shifted namespace index, source-time omission,
both-time omission and wrong type. Separate real-protocol tests check unknown
nodes, restricted reads, writes, untrusted certificate, plaintext sessions and
endpoint policy. Thirty observations must match local records and both consumers;
only good numeric values enter value history. The source ledger checks reference
values/status/time for applicable responses. Timeout/down observations have no
successful source response to match. Gateway-assigned IDs cannot be asserted as
server-generated IDs; they are assigned once after acquisition and preserved.

Reports and public synthetic ledgers are written to artifacts/opcua-pilot. They
record source revision, host OS/CPU/RAM/filesystem and scenario IDs; physical
storage details remain unknown. Private certificate/key/state directories are
removed after child shutdown. Manual reset means stop the mock and start with a
new ledger/private output directory; counter and sessions reset. Never delete
production stores or reuse these short-lived credentials at a real site.

Sources: [SDK v1.4.0](https://github.com/awcullen/opcua/tree/v1.4.0),
[server listener source](https://github.com/awcullen/opcua/blob/v1.4.0/server/server.go),
[OPC Foundation DataValue encoding](https://reference.opcfoundation.org/specs/OPC-10000-6/5.2.2.17).

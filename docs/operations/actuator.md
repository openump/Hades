# Actuator software-pilot operations (Hades-033)

`hades-actuator` is a separate Go write-control service. It defaults to L0 shadow
mode. This implementation supports **literal loopback Modbus TCP mailbox
simulation only**, one active mailbox binding per service, unit ID 1, uint16 engineering-unit setpoints and FC3/FC6. It
cannot connect to a production PLC or write coils/direct outputs. It does not
replace a safety PLC, E-stop or ladder interlock. No real hardware or deployed
Zeus qualification is claimed.

## Reviewed inputs

Provision outside Git: metadata snapshot, actuation policy, public keyring
(`{"operator-key":"<base64 32-byte Ed25519 public key>"}`), private receipt key
(base64 64-byte Ed25519 private key, mode 0600), mTLS credentials and mailbox
bindings. Never use synthetic contract-fixture signatures as credentials. All
signed bytes follow [Hades-032](../contracts/write-control-plane.md): JCS/RFC8785,
Ed25519 and the specified domain separators. Public keys are loaded locally;
commands cannot supply or fetch trusted keys.

A binding array associates an asset/parameter with a reviewed `IP:port` and an
additional PLC-owned applied-request sequence register:

```json
[{
  "asset": "urn:openump:acme:berlin:press-04",
  "parameter": "pressure_setpoint",
  "endpoint": "127.0.0.1:1502",
  "ack_sequence": "holding:105"
}]
```

The policy supplies distinct proposed/request/watchdog/active/reject registers.
The applied sequence is necessary: an unchanged active value alone cannot prove
that a specific new command was accepted. Numeric aliases and physical register
overlap across bindings are rejected, including overlap with readback registers.
Other address families permitted by the declarative schema remain unsupported
by this software driver. There is no address taken from a command payload.

The approved metadata must include the current PLC-active value as a measurement
with the same name and unit as the writable parameter, plus all policy
precondition measurements. The stock telemetry example lacks
`pressure_setpoint`; it will therefore fail closed for that example policy until
a matching reviewed telemetry context is provisioned. A good-quality reading must
have a known clock uncertainty, nonfuture source timestamp, current approved
metadata revision, and fresh source and local receipt times. The current value
must be younger than one second; preconditions also enforce their policy max age.
Unknown clocks never authorize a write.

## Gates and ownership

1. Validate the strict command schema/topic, verify signature with a trusted key,
   bind the complete signed command to its JCS SHA-256 digest, and reject retained,
   future-issued and expired commands (`age >= ttl_ms`). A consumed ID cannot
   execute again; identical retries return the stored receipt, changed bodies
   conflict. This applies across restarts.
2. Check policy revision, enterprise/site, actor/key pair, requested autonomy,
   site/asset ceilings and parameter allowlist. L0 emits `SHADOW_LOGGED` with no
   fieldbus calls. L1 persists `AWAITING_OPERATOR_CONFIRMATION`; it does not write.
3. For execution, require fresh current-value telemetry, matching units, hard
   range, integer uint16 driver range, per-step delta and cooldown. Values are
   rejected rather than silently clamped. Attempts that might have staged a
   value consume cooldown even when readback is uncertain.
4. Require every policy precondition unchanged in the command, and evaluate all
   supplied conditions against good, fresh, same-asset telemetry. Recheck
   freshness/lease after audit sync. Broker disconnection locks shadow mode for
   the remainder of the process; a command never renews its original lease.
5. Append and fsync a signed, hash-chained audit intent containing the immutable
   command, gates, telemetry snapshot and monotonic request sequence **before**
   the first heartbeat/staging packet. Audit failure refuses I/O and locks shadow.

The audit reuses the bounded synced segmented WAL: 10,000 entries, 64 MiB and a
64 MiB filesystem reserve, with additional headroom before execution. It never
evicts replay protection or silently resets an exhausted request sequence.
Heartbeat entries count toward these bounds. Full audit requires an operator to
stop the service and preserve/reconcile its audit and PLC sequence before any new
instance is commissioned; deleting state is not an approved rotation procedure.
Signed hash chains detect modified records and missing interior entries; export
and independently anchor the observed final chain head to detect tail rollback.
The local disk alone cannot prove its own complete history to a privileged attacker.
Receipt/audit key changes require preserving the old audit and its verification
identity; startup fails closed if its signatures no longer verify.

A crash after a synced intent produces a signed terminal `TIMEOUT` on recovery;
it never automatically replays the mailbox write or resumes a heartbeat lease.
`TIMEOUT` means unknown/unverified outcome, requiring PLC/operator reconciliation.

## PLC-owned handshake and watchdog

After syncing the gate snapshot, the edge reads PLC-active value and the prior
applied sequence. It refuses a reused sequence and rechecks actual PLC slew/bounds,
then syncs that preflight before sending heartbeat, proposed value and request
sequence. A fresh audit directory must never reset an existing PLC sequence.
The simulated PLC must independently check its interlocks, apply/reject the
proposal, and expose active value, reject code and matching applied sequence.
Readback samples the applied sequence before and after active/reject reads to
reject torn observations. Only a matching sequence, zero reject code and matching
active value yields `EXECUTED`; a matching nonzero rejection yields
`PLC_INTERLOCK_REFUSED`. Missing/mismatched/late readback yields `TIMEOUT`.

After verified execution, heartbeat updates run every 250 ms, only through the
original command TTL and while policy interlocks stay fresh. Each periodic
heartbeat has a synced audit entry. Shutdown/cancellation, kill, broker loss,
stale interlocks or audit/I/O failure stop heartbeat renewal. The PLC simulator
must revert to its local default when its watchdog window expires. This service
cannot force physical fallback after a stopped network connection: that behavior
must exist and be separately qualified in the PLC program. An in-flight packet
cannot be recalled by a kill switch.

## Run, supervise and operate locally

```sh
go build -o artifacts/hades-actuator ./cmd/hades-actuator
artifacts/hades-actuator -state /private/site/actuator \
  -config /private/site/metadata.json -policy /private/site/actuation-policy.json \
  -keys /private/site/operator-public-keys.json \
  -receipt-key /private/site/actuator.key -receipt-key-id actuator-key \
  -bindings /private/site/mailboxes.json \
  -broker tls://127.0.0.1:8883 \
  -tls-ca /private/site/ca.crt -tls-cert /private/site/hades-actuator.crt \
  -tls-key /private/site/hades-actuator.key
```

Without `-enable-write-pilot`, this remains shadow even if policy allows L2/L3.
Use that flag only with the reviewed disposable loopback mailbox fixture.

Run through `hades-service` using its standard reviewed definition and systemd
unit flow: bind the actuator binary SHA-256, all policy/metadata/schema/public and
private credential files and binding hashes in `files`; set `ready_pattern` to
`actuator_ready`, `finite:false`, private `state` for the wrapper, and include the
actuator state directory in `writable_paths`. Readiness follows successful MQTT
SUBACK and the local control socket. `hades-service` provides its digest-bound
`live.sock` status; actuator health JSON also reports shadow lock, pending local
confirmations, audit occupancy and watchdog lease. Keep Unix socket paths short
enough for the host's Unix-domain path limit.

```sh
artifacts/hades-actuator -state /private/site/actuator -action health
artifacts/hades-actuator -state /private/site/actuator -action kill
artifacts/hades-actuator -state /private/site/actuator -action confirm \
  -command-id '<pending UUIDv7>' -command-sha256 '<exact signed-command digest>'
```

The mode-0600 local `control.sock` is the operator authority boundary; its parent
must be mode 0700 and accessible only to the service/operator account. Confirmation
rechecks the original lease and all execution gates. Network/cloud callers have
no confirmation endpoint. Kill cancels active execution and sets all assets to
L0 immediately in process, then syncs `shadow.lock` before acknowledging. The
lock persists across restart. There is no online promotion/unlock action. An
operator must stop/reconcile the service before clearing the lock and reviewing
a restart. A stale control socket is not removed automatically: verify that its
owner stopped before removing it.

## Receipt publication and cloud transfer

Signed receipts are synced in the audit and republished from an outbox until
MQTT QoS1 PUBACK. The `hades-actuator` mTLS role can publish only its receipt
subtree; it cannot publish commands. Commands are nonretained QoS1. Oversized,
invalid or unverified proposals cannot authorize I/O; unprocessable command intake
fails the worker closed and requires diagnosis rather than acknowledging lost
work. Telemetry remains on the existing exact `/telemetry` topic, not an invented
`/telemetry/+` namespace.

`hades-transfer -command-receipt-keys /private/site/actuator-public-keys.json`
opts into the scoped receipt subscription and verifies receipt signatures before
synced intake. Without this keyring, receipt intake remains disabled. Pending and
terminal signed receipts receive separate stable transport IDs derived from
SHA-256 of the exact signed bytes; `command_id` and signed payload are unchanged.
The transport ID has UUID formatting for compatibility and is not a creation-time
claim. Original topic/payload/digest and durable-receipt retirement rules remain.
The future Zeus receiver must explicitly support this receipt stream and verify
its trusted signing identity; existing telemetry-only receiver fixtures do not
qualify deployed cloud receipt processing. Broker PUBACK alone is not proof of
synced transfer intake or cloud acceptance.

## Verification

`go test -race ./internal/actuator ./internal/transfer` covers gate refusals before
any PLC calls, signature/JCS tampering, L0, local L1 confirmation, replay/cooldown,
restart ambiguity, persistent kill, bounded watchdog shutdown, real TCP staging
and PLC refusal, alias/output-address rejection, plus signed transfer intake and
byte-identical restart/replay delivery. `scripts/smoke-security` additionally runs
the opted-in mTLS MQTT command/telemetry/receipt scenario against its disposable
broker; ordinary unit tests skip this external-broker scenario.

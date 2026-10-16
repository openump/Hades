# OpenUMP write-control contract profile (Hades-032)

This additive v1 profile specifies proposals, policy and receipts. It adds no PLC
writer, agent executor, operator-confirmation workflow or command replay worker.
Existing acquisition, normalization, historian and cloud-transfer services remain
on their read-only telemetry path. Runtime execution belongs to Hades-033.

## Contracts and validation boundaries

- `command.schema.json`: UUIDv7 command identity; integer UTC `issued_at_ms`;
  mandatory `ttl_ms` in 1–60000 ms; policy revision; asset URN; named parameter,
  numeric value and unit; origin actor, requested autonomy and signature metadata;
  one or more structured preconditions. Unknown fields are rejected. Numeric
  setpoints are bounded to ±10^12 in this initial profile.
- `actuation-policy.schema.json`: enterprise and physical site scope, site autonomy
  ceiling, per-asset ceiling, approved actor/key pairs, writable parameter allowlist,
  hard envelope, positive per-step delta, cooldown and required preconditions.
- `command-receipt.schema.json`: command identity and SHA-256 binding, asset,
  parameter, policy revision, record time, state, terminal flag, nullable pre-write
  and verified post-write values, unit, ordered gates 1–5 and signature metadata.
  `EXECUTED` requires non-null pre/post values and five passed gates. A rejected
  pre-write command or shadow result cannot claim a post-write value.

The Go `contracts.Validator.Validate` and Python `validate_payload` check schema
shape plus policy/receipt cross-field semantics. Inverted bounds, overlapping
mailbox roles, duplicate assets, cross-enterprise assets, autonomy above the site
ceiling and invalid gate ordering are rejected.

Go `ValidateCommandContext` and the Python development oracle `validate_proposal`
also check explicit trusted policy/registry scope and time. At `now_ms`, a proposal
is eligible for further checks only if `0 <= now_ms - issued_at_ms < ttl_ms`.
Future-issued proposals and the exact expiration boundary fail closed. The topic
must match the registry's current location, parameter, enterprise and physical
site. Policy revision, actor/key pair, autonomy ceiling, parameter, unit and hard
value bounds must agree. Policy-required preconditions must be included unchanged;
extra conditions may make a proposal stricter but cannot remove required ones.

These functions **do not authorize execution**. In particular they do not verify
a cryptographic signature, bind the broker identity to the signer, test live
machine state/freshness, establish idempotency, enforce slew/cooldown against a
current value, capture operator approval, or sync an audit intent. Hades-033 must
enforce all these before any fieldbus packet. Schema-valid arbitrary signature
bytes never constitute a verified origin. An unverified proposal must not be
labelled safe or accepted for PLC execution.

## Signature and identity profile

`command_id` uses [UUIDv7](https://www.rfc-editor.org/rfc/rfc9562.html#section-5.7).
The signature metadata declares Ed25519, an allowlisted key ID, RFC8785
canonicalization and standard padded-base64 64-byte signature material. The future
verifier must decode and verify it with a locally provisioned trusted public key;
never fetch a key from the command or trust self-declared `actor_id`.

Signing bytes are the ASCII domain separator `OpenUMP command v1\n` followed by
UTF-8 [JCS](https://www.rfc-editor.org/rfc/rfc8785.html) of the command with only
`origin.signature.value` removed. All other fields, including key ID, autonomy,
policy revision, TTL, preconditions and target, remain signed. Receipt signing uses
`OpenUMP command-receipt v1\n` and removes only `signature.value`. Receipt
`command_sha256` binds the JCS representation of the complete original command,
including its signature. These signing rules are specified here, not implemented
by JSON serialization or a signing service in this ticket. Structural fixtures
use synthetic signature bytes and all-zero example digests; they are not valid
credentials or signed execution evidence.

A proposal ID must remain immutable across retries. No retained command or
persisted backlog grants a renewed lease. Receipt timestamps are recording times,
not substitutes for device readback timestamps or a durable cloud receipt.

## Autonomy and receipts

| Level | Required runtime behavior |
|---|---|
| L0 | Shadow/advisory only; log `SHADOW_LOGGED` with zero fieldbus writes. |
| L1 | Queue explicit local confirmation; `AWAITING_OPERATOR_CONFIRMATION` is nonterminal. Confirmation must bind the exact command and still-valid lease. |
| L2 | Bounded optimization under site/asset ceilings, allowlist and all five gates. |
| L3 | Supervisory closed loop with the same hard constraints and PLC interlocks. |

The effective maximum is the lower site/asset ceiling; a command cannot promote
itself. A receipt state is one of `SHADOW_LOGGED`,
`AWAITING_OPERATOR_CONFIRMATION`, `EXECUTED`, `REJECTED_EXPIRED_TTL`,
`REJECTED_SAFETY_BOUNDS`, `REJECTED_PRECONDITION_FAILED`, `PLC_INTERLOCK_REFUSED`
or `TIMEOUT`. Only the confirmation-pending state has `terminal:false`.
`TIMEOUT` represents an unknown/unverified outcome, never execution success.
A receipt's signature/gate claims still require a trusted verifier; schema shape
cannot prove a PLC write or readback actually happened.

Preconditions are bounded comparisons (`eq`, `ne`, `lt`, `le`, `gt`, `ge`) over an
approved numeric measurement, unit and maximum age. They are data, not executable
code or arbitrary expression strings. Runtime must require good-quality, fresh,
correct-context readings and fail closed on missing/unknown clocks or values.

Mailbox roles are `proposed_value`, `request_sequence`, `watchdog_counter`,
`active_value` and `reject_code`. The initial address notation permits only
`holding:<address>`, `db:<db>:byte:<offset>` or `ua:<approved-node-key>`; direct
`Q` output syntax is rejected. These are declarative role mappings, not proof
that the PLC program implements safe staging. Hades-033 must resolve driver-specific
addresses/ranges, check overlapping physical regions, verify approved PLC ladder
interlocks and watchdog behavior, and never treat mailbox naming as a safety gate.

## MQTT and ACL separation

Under the existing registry-resolved five-segment asset prefix:

```text
openump/v1/<enterprise>/<site>/<area>/<line>/<asset>/command/<target_parameter>
openump/v1/<enterprise>/<site>/<area>/<line>/<asset>/command_receipt/<target_parameter>
```

Control topic validation is a separate API. Existing `ValidateTopic` remains limited
to telemetry, metadata and status. Commands/receipts must use QoS 1, non-retained
publication; a future actuator must reject retained commands and apply TTL at
execution time. Broker ACLs scope channels, not JSON values, signatures or TTL.

| Identity | Command channel | Receipt channel |
|---|---|---|
| `command-proposer` | Publish approved asset proposals | Read approved asset receipts |
| `hades-actuator` | Read approved asset proposals | Publish approved asset receipts |
| gateway, normalizer, history, transfer | No publish/subscribe grants | No publish/subscribe grants |

The secure Compose profile uses the existing default-deny Dynamic Security
[roles](../../config/broker/security/roles.json). The new identities have only the
reference press-04 scope; there is no fleet wildcard or fieldbus capability.
The development PKI issues their test certificates only with `-test-identities`.
The optional [static ACL template](../../deploy/mosquitto/acl.conf) expresses the
same separation for a certificate-identity listener; it is not auto-loaded by
Compose. [Mosquitto's documentation](https://mosquitto.org/documentation/dynamic-security/)
describes separate publication, receive and subscription checks.

The anonymous development profile explicitly denies command and receipt subtrees,
even though it allows the existing telemetry development namespace. It cannot be
used to enable the write plane. Existing one-level consumer wildcard subscriptions
do not include the deeper command/receipt topics. `hades-transfer` remains unable
to consume receipts; future cloud receipt export needs a separately scoped identity
and deliberate contract change, not a broadened telemetry role.

## Compatibility and verification

The existing four schema files and telemetry/context/wire/topic fixtures retain
all nine prior baseline hashes. The baseline adds only three control schemas and
four control fixture files. New site manifests include all seven schemas. An old
four-schema bundle can still compile the telemetry validator, but creating and
validating a new site bundle requires the three additive resources; regenerate a
reviewed bundle rather than silently changing a signed manifest/archive. No
migration of runtime queues is required for this contract-only change.

`./scripts/check-contracts` covers shared shape fixtures. Go control tests and
`tests/test_control.py` check expired/future leases, unit/envelope/autonomy/origin
scope and missing/changed policy interlocks. `scripts/smoke-security` proves actual
broker denials for gateway/normalizer/history/transfer on both control subtrees,
permits proposer commands and actuator receipts, and rejects the reverse roles
and cross-asset publication. These MQTT probes use inert text, never execute a
command and never connect to a PLC write interface.

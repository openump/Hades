# OpenUMP v1.0 telemetry

**Status:** Initial contract implemented by Hades-004. The white paper's envelope is illustrative; the schema here adds explicit metadata revision and clock/sequence context before protocol stabilization.

## Encoding and validation

Publish a UTF-8 JSON object of at most 65,536 bytes, including whitespace. Reject duplicate object keys, invalid UTF-8, non-finite values (`NaN`, infinities, overflowing exponents), and malformed JSON before schema validation. Limits apply before parsing. Measurement count is 1–64. Producers must split larger observations into separate messages with distinct IDs.

Use [JSON Schema Draft 2020-12](https://json-schema.org/draft/2020-12/json-schema-validation) with format checking enabled, not annotation-only handling. Resolve the local schema IDs without network access. Validate [telemetry.schema.json](../../schemas/v1/telemetry.schema.json) and [common.schema.json](../../schemas/v1/common.schema.json). Format assertions must check calendar validity and UUID syntax as well as the schema patterns. The foundation helper is `tools.contracts.validate_payload`; Hades-006 will provide the complete CLI/CI integration.

## Required envelope fields

| Field | Contract |
| --- | --- |
| `schema_version` | Exactly `1.0`; topic major version is `v1`. |
| `message_id` | Lowercase UUID unique across the enterprise; allocate a fresh ID per observation and preserve it byte-for-byte during retry/replay. Same ID with different content is a conflict, not an update. |
| `asset_id` | Immutable allocation identity defined in [namespace.md](namespace.md); validate topic matching with the applicable registry. |
| `metadata_revision` | Positive integer referencing the exact asset metadata snapshot used to interpret the reading. |
| `observed_at` | UTC timestamp in `YYYY-MM-DDTHH:mm:ss.sssZ`, recording the device time when available or the gateway acquisition time otherwise. |
| `ingested_at` | Same UTC format; arrival at normalization, not broker acknowledgement or storage commit. |
| `source` | Gateway ID, protocol name, boot ID, sequence, clock origin, and uncertainty. |
| `measurements` | Named values with explicit units and quality. |

All integers used for revision and sequence are bounded to JavaScript's exact-integer range (up to 9,007,199,254,740,991). Sequence starts at zero or a documented initial value and increases for newly acquired observations within `(gateway_id, boot_id)`. A restart, wrap, or sequence reset requires a new boot UUID; retries retain the original sequence. Sequence is diagnostic ordering evidence, not a substitute for message identity or a global ordering guarantee.

`clock_origin` is `device` or `gateway`. `clock_uncertainty_ms` is a nonnegative estimate bounded to one day, or null for unknown/unbounded uncertainty. Producers must report the estimate honestly; zero asserts no known uncertainty and is not the default for an unsynchronized clock. Consumers assess skew/freshness separately. The schema does not require observation time to precede normalization time because independent clocks can disagree; do not silently replace the source time.

## Measurement semantics

Names use lowercase snake case, start with a letter, and contain at most 64 characters. Values may be finite numbers between -1e308 and 1e308, booleans, strings up to 256 characters, or null. The applicable metadata fixes each measurement's type and unit; consumers must reject/quarantine conflicts instead of coercing strings or changing units silently.

Quality is `good`, `uncertain`, or `bad`. A null value requires `bad` quality and a nonempty reason (at most 256 characters), such as `device-timeout`. Missing acquisition results should be represented this way when a reading was attempted. Omitting a measurement means it was not included in that message; it is not a zero, a null sample, or proof of a healthy signal. A bad reading may retain a finite diagnostic value, but must not be used as a good observation. Non-null values may have an optional reason.

The initial supported case-sensitive [UCUM](https://ucum.org/ucum) subset is `1`, `%`, `Cel`, `K`, `bar`, `Pa`, `kPa`, `m`, `s`, `Hz`, `V`, `A`, and `W`. `1` is used for booleans and dimensionless strings. The list is deliberately explicit; this profile does not claim to validate all UCUM expressions. Additional units require a reviewed contract revision and consumer rollout. Range validation and type-to-unit agreement require metadata and are outside structural telemetry validation.

## Unknown fields and extensions

Core objects are closed: unknown envelope, source, and measurement fields are errors. An optional envelope `extensions` object accepts at most 16 namespaced keys (`vendor.key`), each with a bounded scalar value. Keys use lowercase identifiers; values are strings up to 256 characters, finite bounded numbers, booleans, or null. Extensions cannot redefine core meaning or carry secrets. Consumers may ignore them. Nested arbitrary objects are prohibited.

## Delivery

Default telemetry publication uses QoS 1 and no retention. This provides at-least-once transport behavior, not exactly-once history or durable cloud acceptance. Durability, deduplication, buffering, and replay boundaries are specified by Hades-007. Current consumers should reject unsupported schema versions and signal missing metadata rather than invent context.

## Fixtures

[Telemetry cases](../../examples/contracts/telemetry/cases.json) include numeric, boolean, string, uncertain, bad/null, and extension messages, plus invalid timestamps, identifiers, unknown fields, counts, units, and missing values. Run the documented unit-test command in README. Strict wire JSON and byte-limit tests supplement schema fixtures.

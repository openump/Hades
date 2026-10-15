# Hades-026/027: foundations and remaining acceptance

This change starts the two optimization tickets. It does not mark either ticket
complete. Existing history/transfer storage remains v1; no automatic migration or
storage downgrade is performed. OPC UA implementation in PR #13 is a dependency.

## Opt-in segmented log API

`storage.OpenWAL` creates a separate private v2 directory with a versioned FORMAT
manifest, exclusive lock, 4 MiB target segments and a compact resident location
index. `Commit` takes 1–256 unique-key mutations (≤1 MiB encoded per transaction,
≤100 KiB per value). It writes a length/CRC32 framed transaction and returns only
after one successful fsync. Callers select their batching policy; this API never
acknowledges an unsynced timer buffer. A failed write/sync poisons the handle.
Updates/deletes are transaction mutations. State-specific lightweight markers are
not implemented yet: a value update still writes its complete supplied value.

Recovery scans segments in sequence rather than opening each record file. Only
an incomplete final frame in the final segment is truncated; checksum failures,
invalid frames and torn sealed segments fail startup visibly. It does not attempt
to hide corruption or recover an invalid transaction. CRC detects accidental
corruption, not hostile tampering. The resident index holds key, segment, offset,
value length and transaction item index. Get reads/checks the corresponding frame;
reading a member of a batch currently decodes the whole bounded frame.

Limits retain caller-selected record/byte caps, up to 100,000 records and 256 MiB
accounted live bytes. Current accounting includes the existing 4096-byte record
charge, so that byte cap may bind well before the record cap. The log refuses
writes at twice its configured live-byte cap rather than growing without limit.
Disk admission checks caller-selected reserve before commit. No default reserve
change is silently applied to existing stores.

Explicit `Compact` is maintenance-window copy/rewrite, bounded by the live store
limits. It requires two live-cap allowances of free space above reserve, syncs a
new generation, atomically publishes and syncs FORMAT, then deletes the previous
generation. An interrupted prepublication copy remains unselected; the original
is authoritative. A future compaction attempt removes its abandoned next-generation
files. No live background compactor, runtime state migration or backup format
conversion is provided in this slice. Do not use this experimental API for a
production durable-acceptance boundary until those integrations are reviewed.

Focused checks cover batch refusal, recovery, delete persistence, incomplete tail,
checksum corruption and compaction. `go test ./internal/storage -run '^$' -bench
BenchmarkWALGroupCommit10000 -benchtime=1x -benchmem` reports 10,000-record write
and recovery timing plus fsync count. It is not the required full 256 MiB resident
heap/RSS comparison or a physical durability/endurance qualification.

## Catalog and configurable ingress identity

`catalog.Load` accepts 1–256 explicit metadata snapshots, rejects duplicate asset
identities and unsupported protocols, validates numeric definitions with all
contract-approved units, and constructs a shared topic registry. Context lookup
requires the exact asset/revision. `Validate` checks schema, catalog ownership,
exact topic, measurements/units and metadata snapshot. It uses the immutable
schema-validated JSON representation, without floating-point metadata comparison.
This API is a foundation; normalizer/history/transfer are not yet multiplexed
through this registry. The single-context defaults remain valid.

The normalizer now supports `-ingress-writers` and `-ingress-readers`, comma-separated
exact verified certificate CNs. Defaults preserve the reference gateway/admin
identities. Wildcards, duplicates, empty writer lists and malformed identities
are rejected; unverified requests remain forbidden. Granting HTTP access does not
authorize an asset: schema/context/topic validation still applies. URI SAN policy
and per-identity asset scope are not implemented by this CN-only slice.

## Required follow-up within the existing tickets

Hades-026: integrate v2 storage into history/transfer with lazy disk payloads,
lightweight state markers, explicit stopped-store migration, versioned backup/
restore and existing recovery/capacity scripts; verify interrupted migration and
compaction, 100,000-record recovery, 256 MiB heap/RSS, actual fsync and baseline
comparison. Existing v1 adapters still duplicate payloads in memory.

Hades-027: wire the catalog into one running normalizer/history/transfer deployment,
explicit scoped CN/SAN rules, protocol driver interface with reviewed configurable
Modbus/OPC UA mappings, multi-asset integration/negative tests. Current lab-only
endpoint restrictions remain; configuration is not permission for physical PLC
access. No write/actuation capability is introduced.

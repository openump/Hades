# OpenUMP v1 namespace and asset identity

**Status:** Initial normative profile for Hades-003; changes follow the versioning policy introduced in Hades-004.

## Publication topics

```text
openump/v1/{enterprise}/{site}/{area}/{line}/{asset}/{message_type}
```

Topics MUST have exactly eight segments and at most 512 UTF-8 bytes. The fixed prefix is case-sensitive. Each hierarchy segment MUST contain 1–64 lowercase ASCII letters, digits, or hyphens, starting and ending with a letter or digit. Spaces, Unicode, percent encoding, slashes, empty segments, MQTT wildcards, and case folding are prohibited. No decoder or normalization step changes an incoming topic into a valid topic. Message types are exactly `telemetry`, `metadata`, and `status`.

This is an OpenUMP equipment mapping convention inspired by ISA-95, not ISA-95 conformance. A site maps its enterprise, site, area, and line explicitly. Equipment without a production line uses a documented `unassigned` grouping. Every asset is represented at the final asset level; more detailed relationships belong in metadata.

Subscription filters may use MQTT wildcards according to broker authorization. A subscription filter is not a publication topic and is not accepted by the publication validator. See the [MQTT 5 standard](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html).

## Stable identity

An asset ID is `urn:openump:{enterprise}:{identity_site}:{asset_key}`. Its three variable segments use the same 1–64 character rules. `identity_site` is the allocation scope at creation, not a continuously updated location. The asset key is unique in that immutable scope and MUST NOT be recycled for replacement equipment. Enterprise ownership is immutable for v1; transfer to a different owner allocates a new ID and records the historical relationship explicitly.

Display names are editable and are never identity keys. A topic's asset segment is a location alias, not proof of identity. The trusted registry maps the ID to the current enterprise/site/area/line/asset topic path. No two active assets may occupy the same path, and IDs may not appear twice in the current registry. Publishers MUST send the registered asset ID and matching path. Authentication and authorization are separate from these checks.

## Relocation

Moving `urn:openump:acme:berlin:pump-02` to Hamburg preserves its ID but changes the site segment of its publication path. The site operator updates the registry and increments metadata revision. Publishers switch to the new path; consumers reconcile metadata by ID rather than treating the new topic as a new machine. Old retained metadata/status are cleared with authorized zero-length retained MQTT publications. Those deletion publications are transport operations, not schema-valid messages. Historical data keeps its original topic and metadata revision.

The fixtures validate the current registry snapshot. Historical replay must validate against the applicable historical mapping, not reject an old valid path merely because equipment has since moved. The delivery/replay design in Hades-007 defines how that mapping is retained and selected.

## Fixtures and verification

[assets.json](../../examples/contracts/assets.json) is a trusted development registry with one relocated example. [topics.json](../../examples/contracts/topics.json) contains positive and negative publication cases. Run:

```sh
python3 -m unittest discover -s tests -p 'test_namespace.py' -v
```

The foundation check rejects duplicate IDs and paths, unknown assets, invalid segments, and topic-to-asset mismatches. Hades-006 will integrate these checks with the conformance CLI and CI. A deployment must validate its registry itself before using it as trusted input.

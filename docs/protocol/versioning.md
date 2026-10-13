# Contract versioning and compatibility

The topic prefix (`v1`) identifies the major compatibility family. `schema_version` identifies the exact supported payload profile (`1.0` initially). Schema resource IDs include the exact profile and message type, for example `urn:openump:schema:1.0:telemetry`; these are identifiers, not remote fetch URLs.

Consumers MUST explicitly support the incoming profile. The initial validators accept only `1.0`. Reject or quarantine unsupported versions with diagnostics; never interpret them as the closest supported profile. Deploy schema files, normalizer configuration, registry, and consumers as a reviewed compatible set. Historical metadata snapshots and schemas must remain available for supported replay windows.

Breaking changes to core meaning, field types, required fields, namespace layout, or identity rules require a new topic major version and a migration plan. During migration publish to distinct major-version topics and document consumer cutover; avoid accidental double-counting in historian adapters. Never silently rewrite archived observations.

Compatible extension data uses the existing bounded `extensions` container. Documentation clarifications and additional test fixtures do not change the payload version. Adding new accepted units, enum values, or fields is not automatically safe: closed `1.0` schemas will reject them. Such changes require a new supported profile, capability/rollout coordination, and explicit compatibility review; incompatible meanings require a new major version.

For a minor profile upgrade, deploy consumers that recognize both profiles first, then producers, and retain the old profile for the agreed migration/replay period. The capability advertisement or configuration mechanism is a later integration decision; do not infer support from connection success. Configuration revisions and contract versions are distinct.

No stable `1.0` release has been published yet. Changes during this foundation phase still require fixture updates, tests, review, and records of deviations from the illustrative white paper. Hades-006 will add automated compatibility/drift checks to the existing contract tests.

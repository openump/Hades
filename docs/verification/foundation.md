# Hades-001–005 verification record

**Date:** 13 October 2026

## Hades-001

Created a clean local clone at the Hades-001 commit, followed README's clone/copy setup, ran `git diff --check`, and confirmed `.env` is ignored and `.env.example` trackable. No credentials were supplied. Project-license choice remains explicitly pending, as permitted by the ticket, and must be resolved before release.

## Hades-002

Native probes passed on macOS arm64 using Mosquitto 2.1.2, Bento 1.22.0, Telegraf 1.40.2, and community VictoriaMetrics 1.154.0. Commands are in the [probe README](../../examples/component-probes/README.md), and version/hash results are [recorded](../../examples/component-probes/results.json).

Verified MQTT QoS 1 pub/sub, raw-value scaling with asset/time preservation, Modbus function 3 read/scaling against a local fixture, and Prometheus import followed by numeric query. Docker daemon unavailable; exact Mosquitto Docker manifest also unavailable. Container packaging, Linux runtime, real OT access, TLS/authentication behavior, durability, and benchmarks remain unverified and belong to later tickets.

## Hades-003

Namespace tests failed before the validator existed, then passed after implementation. Sixteen publication fixtures cover supported message types, relocation, unknown/mismatched assets, invalid segments, wildcards, percent encoding, Unicode, and unsupported versions. A separate test rejects duplicate registry identity. The fixture registry is trusted input; complete registry validation and CLI/CI integration belong to Hades-006.

## Hades-004

Telemetry tests failed before the payload validator existed, then passed. Twenty-eight fixtures cover required fields, timestamps, UUIDs, quality/null semantics, units, count limits, strings, and extensions. Wire tests reject non-finite values and duplicate fields in otherwise valid telemetry, invalid UTF-8, malformed/root-array JSON, oversized UTF-8, and arbitrary schema selection. Boundary tests accept exactly 65,536 bytes and reject 65,537 bytes. Local Draft 2020-12 schema validation enables format assertions.

## Hades-005

Context tests failed before semantic helpers/schemas existed, then passed. Twenty-six fixtures cover metadata ownership, relocation, authoritative relationship fields, effective periods, numeric ranges, measurement types, and producer heartbeat/shutdown/will constraints. Separate checks verify exact metadata revision, source/type/unit/range agreement, boolean-versus-number handling, and stale/future/unknown-clock semantics.

## Repeatable checks

```sh
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements-dev.txt
.venv/bin/python -m unittest discover -s tests -v
.venv/bin/python -c 'import json; from pathlib import Path; from jsonschema import Draft202012Validator; [Draft202012Validator.check_schema(json.loads(p.read_text())) for p in Path("schemas/v1").glob("*.json")]'
git diff --check
```

The final foundation suite has ten test methods exercising 70 named fixtures plus wire/context boundary cases. The four schema resources pass their metaschema checks. The white paper remains unchanged. No tests claim that the eventual MQTT lifecycle, complete industrial pipeline, production deployment, or Zeus transfer are implemented.

Review found that regex `$` can match before a final newline. Added failing newline fixtures and made schema patterns require the absolute end with a portable negative lookahead; both regressions now pass. The final README setup and schema suite were also checked in a clean local clone with only requirements-dev.txt installed.

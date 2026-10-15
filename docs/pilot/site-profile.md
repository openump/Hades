# Selected pilot: OPC UA mock server

**Selection:** Owner-selected software pilot, 2026-10-15.
**Implementation status:** Not implemented or verified yet.

The OPC UA mock server replaces Siemens S7-1200 / Modbus TCP as the active
pilot target. Hades-014/015 now qualify a repeatable software source and its
read-only acquisition path. Physical PLC access, Siemens firmware behavior and
physical-device qualification are outside this pilot. Existing Modbus lab
fixtures remain regression tests; their results do not qualify OPC UA.

## Profile to implement and verify

| Item | Selected scope / evidence still needed |
| --- | --- |
| Source | Local isolated OPC UA mock server; exact implementation/version/artifact hash to select and pin |
| Endpoint | Loopback or isolated disposable test network; actual endpoint and application URI recorded by the implementing ticket |
| Address space | Versioned deterministic namespace URI and NodeIds; resolve namespace indexes at connection time |
| Initial signals | Numeric temperature, monotonically increasing counter and machine-state value; define exact OPC UA types, units, ranges and OpenUMP mappings |
| Acquisition | Explicit allowlisted reads; choose and document polling or subscriptions, sampling/update rates, deadlines and reconnect limits |
| Quality/time | Preserve OPC UA StatusCode and source/server timestamps under an explicit mapping; unavailable values remain null/bad/uncertain, not fabricated zeros |
| Security | Pinned application identities and certificate trust; authenticated encrypted profile, restricted read identity, and negative read/write authorization tests |
| Reference values | Deterministic source ledger and expected values, timestamps, quality and scenario transitions |
| Fault scenarios | Bad/uncertain quality, missing node, timeout, disconnect/restart and recovery without uncontrolled retry bursts |
| Host | Record actual OS/CPU/RAM/storage and network; software pilot does not remove capacity evidence requirements |
| Lifecycle | Reproducible start/stop/reset, private credentials, bounded logs/state and cleanup of only owned test resources |

No implementation, endpoint, certificate policy or package version is implied
by this selection. Existing Go gateway code is Modbus/lab-specific and needs an
OPC UA path. Follow ADR-0003 for deployed custom code; evaluate upstream server
and client capabilities before selecting dependencies. Avoid a new platform
runtime without recording its maintenance/resource tradeoff.

## Acceptance boundary

Close Hades-014 after the mock implementation/version, node map, host, security,
rates and expected values are documented and reviewed. Close Hades-015 after
actual OPC UA source → gateway → normalizer → MQTT → history/consumer tests,
including fault and authorization checks, pass with preserved identities and
correct time/quality semantics. Running the old Modbus fixture is insufficient.

The owner authorizes a local software pilot through this selection. It does not
authorize connecting to real equipment, modifying PLC programs or deploying to
an external site. Operator evaluation and reproducible host measurements remain
required before broader rollout. A future physical pilot needs separate scope.

## Implemented software profile

The initial selection table above is superseded by the pinned, executable
[OPC UA mock v1 profile](opcua-mock.md). Its node map, certificate policy,
acquisition limits, lifecycle and source/fault matrix are implemented. Native
macOS arm64 verification passed; see the implementation PR for final Linux
checks and retained evidence before treating this as reviewed acceptance.

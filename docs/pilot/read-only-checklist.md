# OPC UA software-pilot review checklist

The active pilot is a local OPC UA mock server, selected on 2026-10-15.
This checklist replaces the active Siemens/Modbus selection checklist; existing
Modbus lab tests remain separate regression evidence.

- Pin server/client versions, licenses, artifacts and supported runtime/CPU targets.
- Record endpoint, application URI, namespace URI, deterministic NodeIds and types.
- Define units/scaling/ranges and deterministic expected values for each signal.
- Select polling or subscriptions and document bounded rates, timeouts, reconnect
  and pending-data limits. Resolve namespace indexes after reconnect.
- Establish application-certificate trust and authenticated encryption, protect
  private keys and use a restricted read identity.
- Verify unknown/untrusted clients, unapproved reads and writes are rejected.
  Client code issuing only reads is not proof of server-side write protection.
- Specify source/server timestamp precedence, clock uncertainty and StatusCode to
  OpenUMP quality mapping; never substitute zeros or assert unknown clock trust.
- Exercise good/bad/uncertain values, missing nodes, disconnects, server restart
  and recovery with a source ledger and bounded resources.
- Compare source, normalized, broker, historian and consumer values/IDs/time/quality.
- Record host inventory, configuration revision, commands, output and cleanup.
- Report software-pilot pass/fail explicitly. Do not claim Siemens compatibility,
  physical I/O timing, controller load or real-device acceptance from the mock.

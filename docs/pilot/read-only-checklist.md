# Operator review before first real read

The site operator completes and signs the pilot profile before any real endpoint
is contacted. A passing protocol simulator test is preparation, not authorization.

- Confirm device model/firmware and vendor manual revision; reconcile manual
  register numbering with zero-based wire addresses and exact unit ID.
- Approve each signal, function, type, byte/word order, scaling, unit and valid
  range. Explicitly exclude writes, control commands, broadcasts and discovery.
- Approve polling, timeouts, retry behavior and connection budget; confirm they
  cannot disrupt existing control or maintenance clients.
- Record read-only device/access-gateway permissions and firewall rules.
  A Modbus function-3 client alone does not establish a read-only security boundary.
- Identify the operator, gateway host, service account and emergency stop owner.
  Review the Hades-013 certificates and isolated OT/management network paths.
- Establish a synchronized gateway clock and measured uncertainty. Preserve
  unknown clock trust instead of asserting zero uncertainty.
- Run the lab timeout/device-exception/reconnect fixtures and validate telemetry,
  quality and historian mapping before changing any endpoint configuration.
- Record an access window, safe rollback, baseline reference readings and expected
  error responses. Connect only to the exact approved endpoint and signal map.
- Compare source, normalized and historian results against operator-observed
  values; attach timestamps, metadata/config revision and sanitised evidence.
- Record real-device pass/fail separately from lab checks. Leave Hades-014/015
  open until the required site review and real-device evidence exist.

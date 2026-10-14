# First OT pilot: selection pending

Hades-014 requires a named real device and site-approved read-only scope.
No real pilot device, firmware, endpoint, operator, host or access window has
been supplied. The lab fixture below is not a selected PLC and does not satisfy
real-device acceptance. Do not derive a register map from a generic Modbus example.

## Site selection record to complete with the operator

| Required evidence | Current state |
| --- | --- |
| Site/operator and written read-only authorization | Pending |
| Device manufacturer, exact model and firmware | Pending |
| Manufacturer register map/manual revision and link | Pending |
| Approved IP/port, unit ID, network path and access window | Pending |
| Read functions, zero-based register addresses, types and byte order | Pending |
| Scaling/offset, UCUM units and valid ranges for each signal | Pending |
| Maximum poll rate, timeout, retry and device connection limits | Pending |
| Reference readings and operator comparison method | Pending |
| Pilot host CPU/RAM/OS/storage and service account | Pending |
| Gateway/site clock synchronization and measured uncertainty | Pending |
| Device/server permissions or read-only protocol enforcement | Pending |
| Security profile, certificates, rollback and stop authority | Pending |

`config/gateway/pilot.example.json` is an unapproved record template. Missing
fields are intentional; it must not be interpreted as deployment approval.

## Selected foundation candidate and explicit lab scope

The existing ADR-0002 selects Telegraf 1.40.2 with Modbus TCP as the first driver
candidate. The vendor device/firmware documentation must confirm compatibility
before that candidate becomes a real-device selection. The lab is a local TCP
fixture accepting unit 1, function 3 (read holding registers), zero-based register
0, one unsigned 16-bit big-endian value. It returns 672 deci-degrees Celsius,
which normalization scales once to 67.2 `Cel`. No write, broadcast, other unit,
other register, multi-register type or vendor operation is permitted by this
fixture. This does not prove a PLC's register semantics or write protection.

The lab wrapper admits one reading at a time, limits polling to at most 1 Hz,
uses a 200 ms device timeout with no busy retries, and reconnects on the next
scheduled read. Failed reads become null/bad observations with a bounded reason;
there is no last-value/zero substitution. Observation time is the gateway's UTC
poll completion/failure time, not device sample time. Clock uncertainty remains
unknown (`null`) until the site supplies a measured bound.

Modbus TCP itself supplies neither authentication nor read-only privileges.
The gateway's read-only configuration is not a substitute for device/access-gateway
permissions and OT firewall enforcement. Keep the source on the authorized OT
segment and carry observations to Hades through the Hades-013 authenticated
boundary. The current lab executable rejects non-loopback source endpoints.

Sources: [ADR-0002](../decisions/0002-reference-components.md),
[Telegraf 1.40.2 Modbus configuration](https://github.com/influxdata/telegraf/blob/v1.40.2/plugins/inputs/modbus/README.md).

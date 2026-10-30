# PLC4X lab acquisition (Hades-040)

`hades-gateway` can poll a brownfield controller through the in-process Apache PLC4X
Go drivers. This is a **lab path only**. Site approval for real controllers is pending.
The gateway accepts it only when all of the following hold:

1. The binary is built with `go build -tags plc4x ./cmd/hades-gateway`. The default
   build does not link plc4go and refuses `plc4go` contexts.
2. `-lab` is set and mTLS ingress credentials are supplied, as for the Modbus lab path.
3. The metadata context declares `"acquisition": {"protocol": "plc4go", ...}`.
4. `-acquisition` names a gateway acquisition map with a tag address for **every**
   measurement. Addresses are never derived from measurement names.
5. `-source` is a supported scheme with a **literal loopback IP** host
   (`127.0.0.1` or `[::1]`). Hostnames and other addresses are rejected.

The PLC4Go driver applies every Hades-031 guardrail: coalesced reads where the protocol
allows, a per-endpoint token bucket and one-in-flight limit, a circuit breaker that emits
`producer_status`, and per-signal deadbands. The read path exposes no write, subscription
or browse builder.

```sh
go build -tags plc4x -o artifacts/hades-gateway-plc4x ./cmd/hades-gateway
artifacts/hades-gateway-plc4x -lab \
  -config examples/site/plc4x-s7-lab.metadata.json \
  -acquisition examples/gateway/plc4x-s7-lab.acquisition.json \
  -source 's7://127.0.0.1:11102?cotp.remote-rack=0&cotp.remote-slot=1' \
  -ingress https://127.0.0.1:8081/observations \
  -tls-ca ca.crt -tls-cert gateway.crt -tls-key gateway.key
```

On this path `-count` is the number of **polls**, not envelopes. Each poll admits one
envelope per measurement, minus readings a deadband suppressed. The gateway logs
`gateway_health` every second with polls, acquisition failures, admissions, ingress
retries and pending records, and prints `producer_status` on breaker transitions.

## Acquisition map

```json
{
  "signals": {
    "motor_temperature": {"address": "%DB1:0:REAL", "deadband": {"epsilon": 0.5, "max_silence_ms": 60000}},
    "cycle_counter": {"address": "%DB1:10:UINT"}
  }
}
```

Unknown fields are rejected. `max_silence_ms` must be between 1000 and 86400000. Every
signal must be a measurement of the asset.

## Endpoints and tag addresses

These are verified against the pinned plc4go commit and the CI simulators.

| Protocol | Endpoint | Tag address examples | Notes |
|---|---|---|---|
| Siemens S7 | `s7://127.0.0.1:102?cotp.remote-rack=0&cotp.remote-slot=1` | `%DB1:0:REAL`, `%DB1:4:DINT`, `%DB1:8:INT`, `%DB1:10:UINT`, `%DB1:12.0:BOOL`, `%DB1.DBD0:REAL` | Options are `cotp.`-prefixed; unprefixed `remote-rack` is ignored. Offsets are bytes; BOOL uses `byte.bit`. |
| EtherNet/IP (Logix) | `logix://127.0.0.1:44818` | `%Temp:REAL`, `%Count:DINT`, `%Small:INT`, `%Flag:BOOL` | Use `logix://` (little-endian) for ControlLogix/CompactLogix. `eip://` defaults to big-endian. The leading `%` is required. |
| Beckhoff ADS | `ads://127.0.0.1:48898?target-ams-net-id=…&target-ams-port=851&source-ams-net-id=…&source-ams-port=…` | Symbolic `GVL.fReal`; direct `0x4020/0:REAL`, `16416/4:DINT` | All four AMS options are required. Direct reads still need the type in the PLC data-type table. |
| BACnet/IP | `bacnet-ip://127.0.0.1:47808?local-port=47900` | `ANALOG_VALUE,1/PRESENT_VALUE`, `BINARY_VALUE,1/PRESENT_VALUE`, `POSITIVE_INTEGER_VALUE,1/PRESENT_VALUE` | plc4go binds `0.0.0.0:47808` locally by default; set `local-port` when the device uses 47808. BinaryValue reads as an enum (0/1). |
| Modbus TCP | `modbus-tcp://127.0.0.1:502` | `holding-register:1:UINT` | PLC4X Modbus addresses are **one-based**: `holding-register:1` is protocol address 0. Always give the type; the default is signed INT. |

## Measured scan cost

From the `plc4x-simulators` CI job, which publishes a `plc-simulator-evidence` artifact:

| Protocol | Read requests per poll | Session reuse | Reconnect after reset |
|---|---|---|---|
| S7 | 1 multi-item PDU for all tags | 1 TCP session | yes |
| ADS | 1 sum-read for all tags | 1 TCP session | yes |
| BACnet/IP | 1 ReadPropertyMultiple for all tags | UDP | n/a |
| EtherNet/IP (Logix) | **1 per tag**; plc4go sends no Multiple Service Packet | 1 TCP session | yes |
| Modbus TCP | 1 FC03 per contiguous register block | 1 TCP session | yes |

With default guardrails (1 request/s, burst 1, one in flight), 273 caller poll attempts
in 3 s produced 3 controller reads. The other 270 were skipped at the gateway as
`scan-protection` and never reached the PLC.

## Known limitations of the pinned plc4go

- **ADS data race.** `ads.(*Connection).subscribe` writes its subscription map from
  per-tag goroutines without a lock during every connect handshake. The race detector
  flags it on each ADS connect, and in theory it can crash the process with a
  concurrent map write. ADS reads work, but **production ADS use is blocked** until
  upstream fixes it.
- **BACnet negative integers.** SignedInteger present values are not sign-extended, so
  -123456 reads as 16653760. Do not map negative BACnet IntegerValue signals. A guard
  test fails when a plc4go update fixes this.
- **EtherNet/IP requests are not coalesced.** Every tag costs one CIP request. Size
  poll intervals and token buckets accordingly.

## Simulators (test-only)

The `plc4x-simulators` CI job runs these disposable loopback fixtures. They are never
linked into or shipped with Hades.

- S7: python-snap7 3.2.1
- ADS: pyads 3.6.0 with a Hades handler for the PLC4X handshake
- BACnet/IP: bacpypes3 0.0.110
- EtherNet/IP: libplctag `ab_server` v2.7.2, built by `scripts/build-ab-server`

Python fixtures record every write attempt and refuse it. EtherNet/IP writes are counted
on the wire by a CIP-parsing proxy. Negative-control tests prove each detector catches
a real write.

Run locally:

```sh
python3 -m venv /tmp/plc-sim
/tmp/plc-sim/bin/pip install --require-hashes --no-deps -r tests/fixtures/plc-simulators/requirements.txt
./scripts/build-ab-server /tmp/ab_server
HADES_PLC_SIMULATORS=1 HADES_PLC_PYTHON=/tmp/plc-sim/bin/python HADES_AB_SERVER=/tmp/ab_server \
  go test -tags plc4x -run 'Simulator|CIPWire|FixtureLedgers|BACnetNegative|GatedGateway' -v ./internal/gateway
```

# Local commissioning console (`hades-inspector`, Hades-038)

`hades-inspector` serves a strictly read-only diagnostic web console for commissioning an
air-gapped edge node. The UI (vanilla HTML, CSS and JS) is embedded in the binary with
`go:embed`. It needs no Node.js, framework or CDN, and loads fully offline.

```sh
hades-inspector -listen 127.0.0.1:18091 -config examples/site/metadata.json \
  -broker tls://127.0.0.1:8883 -tls-ca ca.crt -tls-cert hades-inspector.crt -tls-key hades-inspector.key \
  -actuator-keys actuator-keys.json -normalizer-health https://127.0.0.1:8081/health \
  -certificates ca.crt,gateway.crt -services /etc/hades/normalizer.json
# then open http://127.0.0.1:18091/ui/ (for example through an SSH port forward)
```

It listens on loopback only. The default port is 18091; 18081 is the normalizer's
ingress. It also refuses any `Host` header other than the loopback address or
`localhost` on its port. A malicious web page that rebinds its own hostname to
127.0.0.1 therefore cannot read plant data through the operator's browser.

`-normalizer-health` needs the normalizer to allow this identity to read health: add
`hades-inspector` to its `-ingress-readers` (the default is `admin` only). If it is
missing, the quarantine panel shows the HTTP error instead of counts.

## Read-only invariants

- **No DNS rebinding:** the `Host` allowlist means only a real loopback origin can read
  the console.
- **No writes:** every route accepts only `GET` and `HEAD`. Every other method gets
  `405 Method Not Allowed`, enforced in one wrapper around the whole handler.
- **No control socket, no PLC access:** `hades-inspector` never opens `hades-actuator`'s
  control socket, which also serves `/kill` and `/confirm`. A compromised browser session
  therefore has no write path one step away. Actuator activity comes from the signed
  receipts on MQTT.
- **Read-only broker identity:** `hades-inspector` may read telemetry, status and
  receipts and publish nothing. `scripts/smoke-security` proves every publish is denied.
- **Strict CSP:** `default-src 'self'`, no inline scripts or styles, no framing. Plant
  values are written with `textContent`, so telemetry cannot inject markup.
- **Bounded memory:** at most 64 assets, declared measurements only, a ring of 32
  receipts, and 4 live streams (a fifth gets 503). A slow or stalled browser misses
  intermediate updates and never blocks the publisher. Measured at full bounds: about
  1.2 MB retained, within the 2 MB budget.

## What each view shows, and what it cannot

The console reads only what Hades already exposes read-only.

| View | Shown | Not shown (and why) |
|---|---|---|
| UNS live wire | Latest contract-validated value, unit, timestamp and quality per declared measurement. A signal silent for more than three expected intervals shows **STALE**. Also the asset's latest status. | Southbound PLC poll round-trip times and guardrail counters. They are in gateway process logs, not on the broker. |
| Actuator receipts and gates | Recent receipts with each gate's result, shown **only when signed by a trusted actuator key** (`-actuator-keys`). A forged receipt is never displayed. | Staging mailbox register state and the watchdog lease. They are available only on the actuator's private control socket, which the console deliberately does not touch. |
| Quarantine | Quarantine and rejection counts and the disk-reserve state, from the normalizer's mTLS `/health`. | Rejected payloads and their violation reasons. They stay on the normalizer's private store, which has no read API. |
| Certificates and services | Certificate subject and days to expiry (`-certificates`), and supervised service readiness (`-services`). | WAL and transfer queue depth and cloud receipt latency. These counters are in-process only. |

Adding read-only diagnostic endpoints to the normalizer, transfer and actuator would fill
these gaps. Each would widen that service's attack surface, so it is a separate, reviewed
change.

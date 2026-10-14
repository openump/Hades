# Service identities and network boundaries

Hades-013 provides a hardened reference profile separate from anonymous
loopback development. MQTT uses mutual TLS with Mosquitto 2.1.2's upstream
Dynamic Security plugin; HTTPS ingress and historian access use Go mutual TLS.
TLS requires a trusted issuer, valid certificate and private key; server names
are verified and TLS 1.2 is the minimum. Cleartext remote URLs, partial credential
sets and credentialed cleartext downgrades are rejected. A secure normalizer also
requires its ingress TLS files. No private material is committed.

## Identities and permissions

| Certificate CN | MQTT | HTTPS |
| --- | --- | --- |
| `gateway-press-04` | No publish/subscribe permission | POST `/observations` on ingress only |
| `normalizer` | Publish this asset's telemetry, metadata, status only | No historian permission |
| `consumer-primary`, `consumer-secondary` | Read and subscribe to this asset's exact `+` filter only | Historian query, query_range and export only |
| `history-adapter` | Read and subscribe to this asset's telemetry only | POST historian import only |
| `admin` | Health topic and Dynamic Security request/response topics | GET health and metrics; no public destructive historian API |

The asset prefix is `openump/v1/acme/berlin/assembly/line-01/press-04/`.
All other ACL operations default to deny. Certificate CN replaces a supplied
MQTT username; client-supplied usernames cannot impersonate another role.
Dynamic Security uses exact `subscribeLiteral` filters and separate
`publishClientReceive` rules. Basic Mosquitto ACL files permit subscription
registration and filter delivery; they do not meet this profile's SUBACK denial
requirement. Only the upstream Dynamic Security plugin is enabled in the source
build; there is no custom C authorization plugin.

HTTP authorization uses verified certificate chains, not headers supplied by a
client or proxy. Unknown roles get 403; missing/untrusted/expired certificates
fail the TLS handshake. The fixed historian proxy forwards only approved read
and import paths. Administrative/debug/delete routes remain inaccessible through
it. MQTT administration requires the admin certificate and separate control-topic
ACLs; the broker's HTTP control API is disabled.

## Local provisioning and startup

For a disposable local evaluation, create short-lived certificates with Go:

```sh
go run ./cmd/hades-pki -out runtime/security/v1
# Build for the Docker host's Linux architecture; use arm64 on an ARM host.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o artifacts/hades-history-proxy ./cmd/hades-history-proxy
export HADES_SECURITY_DIR="$PWD/runtime/security/v1"
docker compose -f deploy/compose.secure.yaml up --build -d
```

The generator refuses an existing output directory. It issues ECDSA P-256
certificates, a 48-hour CA and 24-hour leaf identities with client/server usage
separated and loopback server SANs. The private directory is 0700 and private
keys are 0600. This is a verification CA, not a production trust or enrollment
service. Production certificates must come from the site's controlled PKI, with
correct DNS/IP SANs and these approved role CNs. Do not install machine-role
keys in browsers. Keep issuing keys offline, restrict each service account to
its own private key, and configure expiry monitoring before a real pilot.

The one-shot initializer has no network, a read-only root filesystem and only
CHOWN/DAC_OVERRIDE capabilities. It reads the private source bundle, copies
server material into a named volume, and gives broker files to UID 1883 and
proxy files to UID 10001, both private. Runtime services cannot traverse each
other's key directory. Client keys and the issuing key are never copied into
runtime containers. Existing Dynamic Security policy is preserved on subsequent
initialization; runtime administration does not silently reset to the template.

Run host-side Go services with per-role TLS files, for example:

```sh
go run ./cmd/hades-history -broker tls://127.0.0.1:8883 -historian https://127.0.0.1:8429 -tls-ca runtime/security/v1/ca.crt -tls-cert runtime/security/v1/history-adapter.crt -tls-key runtime/security/v1/history-adapter.key
go run ./cmd/hades-consumer -broker tls://127.0.0.1:8883 -tls-ca runtime/security/v1/ca.crt -tls-cert runtime/security/v1/consumer-primary.crt -tls-key runtime/security/v1/consumer-primary.key
go run ./cmd/hades-normalizer -broker tls://127.0.0.1:8883 -tls-ca runtime/security/v1/ca.crt -tls-cert runtime/security/v1/normalizer.crt -tls-key runtime/security/v1/normalizer.key -ingress-ca runtime/security/v1/ca.crt -ingress-cert runtime/security/v1/ingress.crt -ingress-key runtime/security/v1/ingress.key
```

The gateway sends HTTPS with its own certificate; only admin may read ingress
health. Plaintext ingress exists only in the separate credential-free local
reference. The simulator's direct MQTT mode needs normalizer publisher rights;
it is a development source, not a second privileged production gateway.

## Segmentation and rotation

The secure Compose file is standalone. Never combine it with the anonymous
`deploy/compose.yaml`. Its only published ports are loopback TLS MQTT 8883 and
HTTPS historian proxy 8429. The raw historian has no published port and uses an
internal backend network shared only with the proxy. That same-host backend is
the explicitly trusted cleartext zone; crossing Docker hosts requires TLS there
too. Normalizer ingress binds loopback. Remote site access requires an authorized
TLS tunnel or reviewed site-specific bind/firewall policy, not an open dev port.

Keep PLC/OT networks isolated from enterprise and management networks. Permit
only the selected read-only gateway path from OT, then gateway-to-ingress and
service-to-broker flows. Administrative certificates and Dynamic Security control
access belong on a separate managed workstation/VPN route. No deployed service receives
host networking, Docker socket or OT access. Actual site routes/device permissions
are Hades-014/015 prerequisites; this ticket contacts no industrial device.

For leaf renewal, issue a new key/certificate for the same role and restart that
service with the new files. Loaded Go keys do not hot reload. Drain normalizer
intake and wait for pending=0 before restart; its queue is volatile. History
intake persists across restart. Broker/server rotation closes existing TLS
sessions so previously authenticated connections do not keep indefinite access.
A same-CA old leaf remains valid until expiry or PKI revocation; disable the role
immediately through Dynamic Security where applicable, or replace the issuer to
invalidate old certificates. This profile does not claim live per-leaf CRL/OCSP
support in every Go service.

For an issuer change, pause/drain ingress, distribute the replacement CA and
per-role credentials, stop clients/broker/proxy, and rerun the initializer and
servers with the replacement source directory (`--force-recreate`). Restart
clients with replacement trust and keys. Do not delete broker/history volumes;
expect reconnect, replay and normal duplicate handling. A rollback restores the
previous complete trust bundle and services only if that issuer remains trusted
and uncompromised. Never bypass certificate verification to make rotation work.
The reference test uses a coordinated cutover, not a zero-downtime renewal claim.

## Verification and limits

`./scripts/smoke-security` creates disposable roots and credentials outside Git,
checks permitted/forbidden MQTT writes and SUBACK denials, anonymous and unknown
identity rejection, HTTP read/write/management separation, and the actual Go
mTLS ingestion/history path. It replaces the issuer, verifies old client rejection
under new server trust, proves new Go subscription readiness and inspects the
secure Compose port/backend-network configuration. Its disposable security
volumes are removed; it never touches the developer's unrelated stack.

The native macOS arm64 run passed with source-built Mosquitto 2.1.2 and its
upstream plugin, native VictoriaMetrics 1.154.0 and Go clients. Hosted Linux CI
repeats against containers. Race tests check TLS downgrade refusal and HTTP
route-role boundaries. Existing contract fixtures remain unchanged. Resource
sizing, session/capacity failures and pilot revocation operations still need
Hades-016/017/022 verification; no production-ready or zero-loss claim is made.

Sources: [Mosquitto certificate identities](https://mosquitto.org/man/mosquitto-conf-5.html),
[Dynamic Security](https://mosquitto.org/documentation/dynamic-security/),
[pinned subscription enforcement source](https://github.com/eclipse-mosquitto/mosquitto/blob/v2.1.2/plugins/dynamic-security/acl.c),
[Go TLS configuration](https://pkg.go.dev/crypto/tls#Config).

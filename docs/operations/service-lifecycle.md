# Reviewed host service lifecycle (Hades-024)

`hades-service` supervises one explicitly selected executable, collects its stdout
and stderr, and answers live status on a private Unix socket. It covers the Go
gateway (including its Telegraf child), normalizer, history and transfer workers.
The existing `hades-site` reviewed Compose profile continues to provision broker,
historian and TLS proxy. This is a reference single-host installation, not a fleet
reconciler. An OPC UA gateway is not implemented by this change.

## Prepare a Linux installation

Use a dedicated non-root account per trust boundary. Install reviewed binaries
and configuration in administrator-owned directories, outside the service's
writable paths. Keep leaf credentials private to their role. Do not give a worker
access to the CA private key. Use absolute paths and private, nonsymlink regular
service definitions (0600). Every configuration, mapping and credential referenced
by the command must appear in `files` with its SHA256; include the metadata,
normalization mapping, CA and role certificate/key. Do not include mutable intake
records or active logs. Hashing identifies reviewed content, not a signature or a
replacement for administrator ownership. Updating executable files in place while
a process is running is unsupported: stop, replace and restart instead.

A definition has this shape; substitute real absolute paths and exact SHA256s:

```json
{
  "revision": "site-2026-10-15-a",
  "binary": "/opt/hades/bin/hades-history",
  "binary_sha256": "<64 lowercase hex characters>",
  "args": ["-root", "/opt/hades/reference", "-config", "/etc/hades/metadata.json",
    "-broker", "tls://127.0.0.1:8883", "-historian", "https://127.0.0.1:8429",
    "-intake", "/var/lib/hades/history/intake", "-quarantine", "/var/lib/hades/history/quarantine",
    "-tls-ca", "/etc/hades/history/ca.crt", "-tls-cert", "/etc/hades/history/history-adapter.crt",
    "-tls-key", "/etc/hades/history/history-adapter.key"],
  "directory": "/opt/hades/reference",
  "state": "/var/lib/hades/history/supervisor",
  "ready_pattern": "\"type\":\"history_ready\"",
  "files": {"/etc/hades/metadata.json": "<sha256>", "/etc/hades/history/ca.crt": "<sha256>",
    "/etc/hades/history/history-adapter.crt": "<sha256>", "/etc/hades/history/history-adapter.key": "<sha256>"},
  "writable_paths": ["/var/lib/hades/history"],
  "log_bytes": 1048576
}
```

Use these readiness patterns for the other reference commands: `normalizer ready`,
`"type":"transfer_ready"`, and `gateway_health`. Gateway health is first emitted
after acquisition starts, rather than implying successful device qualification.
Finite development fixtures may set `finite: true`; deployed workers default to
failure on unexpected successful exit so systemd restarts them.

Create the definition's state and writable directories as the selected account,
mode 0700. `state/live.sock` must fit within 100 bytes. Render and install each
unit (example uses the already-created `hades-history` account):

```sh
/opt/hades/bin/hades-service -action digest -config /etc/hades/history.service.json
/opt/hades/bin/hades-service -action unit -config /etc/hades/history.service.json \
  -name hades-history -user hades-history > /tmp/hades-history.service
sudo install -m 644 /tmp/hades-history.service /etc/systemd/system/hades-history.service
sudo systemctl daemon-reload
sudo systemctl enable --now hades-history
sudo -u hades-history /opt/hades/bin/hades-service -action status -config /etc/hades/history.service.json
sudo systemctl stop hades-history
```

Start the reviewed Compose infrastructure first and verify broker/proxy health.
Then start history and optional transfer, normalizer, and finally gateway; verify
each live status before advancing. Stop in reverse order, allowing the gateway
and normalizer to finish before stopping consumers. The reference orchestrator
`benchmark-stack --systemd` performs this sequence on disposable Linux using
units in `/run/systemd/system`, and removes only its own random-named units.

A site must explicitly select transfer enabled with receiver URL and private
cloud trust, or disabled without a worker. Disabled is not receipt delivery.
When enabled, check its independent stats and durable receipts. Normalizer/history
health reports `not_observed_by_this_service`; it cannot certify cloud health.

## Evidence and limits

Live status checks the launch definition digest and each referenced artifact
against the running supervisor's original definition. A stopped socket, changed
revision or changed artifact fails the probe. Status includes actual child PID,
supervisor PID, launch revision, binary digest, start time and last output time.
A readiness string is a startup observation. Continue checking application health,
queue ages, refusal/drop counters and output timestamps; live PID alone cannot
prove downstream progress or functional readiness indefinitely.

Each supervisor retains at most two files of `log_bytes` (256 B–5 MiB each),
0600, plus a lock and 0600 Unix socket in a 0700 directory. Both child streams are
collected. Rotation can split/truncate individual large writes, so these are
bounded diagnostics, not an immutable audit ledger. Periodic history/transfer
stats remain visible; normalizer periodic health is obtained from its protected
`/health` API. Failure of the wrapper is captured by rate-limited journald; set
host-wide journald disk/retention limits separately. The rendered unit uses
CPUQuota=50%, MemoryMax=512M, TasksMax=64, a 12-second shutdown deadline and up to
five starts per minute. The wrapper sends TERM to the child process group, waits
eight seconds then kills it; systemd owns automatic restart and cgroup cleanup.

## Upgrade, rollback and partial failure

Stop acquisition first. Save the previous reviewed definitions and immutable
binary/configuration bundle. Stop affected services, preserve intake/quarantine
and database volumes and their ownership, install the compatible new bundle,
render replacement units and start in dependency order. Probe every service;
record which revision is running. If a start fails, stop that service and restore
the previous complete bundle/unit, then probe again. Never delete a queue to make
a revision start. A changed on-disk definition intentionally makes an old running
revision fail status until it is restored or restarted. Report this as partially
applied, not a successful rollout.

Only same-contract, same-storage-format rollback is covered. Metadata revision,
measurement semantics, queue format and database migrations need a separate
reviewed migration/export plan. Credential rotation uses the existing security
runbook and must not broaden asset authorization.

Verification: `go test -race ./internal/supervisor`, `scripts/smoke-ot-gateway`
(actual supervised Modbus regression), and `scripts/benchmark-stack --systemd`
(installed Linux workers, process kill/restart, live revision mismatch, failed
start, queued-ID preservation, compatible upgrade/rollback). Inspect CI results
before claiming installed Linux acceptance; a macOS native run is not that proof.

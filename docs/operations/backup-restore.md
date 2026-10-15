# Cold backup and isolated restore

Hades-021 provides a deliberately offline recovery path for the pinned
single-site stack. This procedure requires downtime. It does not establish high
availability, a pilot RTO/RPO or protection for unsynced/volatile observations.

## Component methods and boundaries

| Component | Selected method | Recoverable boundary |
| --- | --- | --- |
| Mosquitto 2.1.2 | Clean stop, then copy its entire persistence directory. | Broker's shutdown-written database, including retained messages and surviving sessions. An abrupt pre-autosave crash can lose acknowledged data. |
| VictoriaMetrics community 1.154.0 | Clean stop, copy the whole `storageDataPath`, restore into an empty directory on the same pinned version. | Data included by graceful shutdown; HTTP API acknowledgement alone is not a disk-commit guarantee. |
| History/transfer intake and diagnostics | Stop host adapters, acquire each declared store's exclusive ownership lock, copy complete store directories. | Synced intake/suspension state; original message ID/topic/payload/digest. Runtime counters and volatile stages are not recoverable. |
| Configuration | Copy the private manager state and its reviewed release snapshots. | Manifest, hashes, schemas, metadata, static broker/Compose configuration, default mapping/pilot template and proxy artifact. Independent host service environment files are outside this manager. |
| Dynamic Security | Copy the stopped broker's live `roles.json` separately. | Live role/user policy, which may differ from the original seed. |
| Credentials | Exclude CA/private keys and prepared credential volumes. | Reprovision or rotate through the approved PKI process; rebuild role identity mappings before restarting clients. |

This follows [Mosquitto persistence behavior](https://mosquitto.org/man/mosquitto-conf-5.html)
and VictoriaMetrics' [whole-directory cold migration method](https://docs.victoriametrics.com/victoriametrics/single-server-victoriametrics/#from-victoriametrics).
For online historian backups use its supported [snapshot/vmbackup/vmrestore workflow](https://docs.victoriametrics.com/victoriametrics/single-server-victoriametrics/#how-to-work-with-snapshots).
Do not live-copy a running database or copy snapshot subdirectories as ordinary
files. Existing snapshot symlinks make this conservative cold archive refuse
capture; handle such snapshots with the upstream tools, not manual deletion.

## Capture

Prepare/review the configuration as described in
[configuration rollout](configuration-rollout.md). Declare all persistent host
stores in `-host-state` during preparation, for example:

```sh
artifacts/hades-site -action prepare -profile secure -revision site-a \
  -project hades-site -security-dir /private/issued \
  -host-state '{"history-intake":"/var/lib/hades/history-intake","transfer-intake":"/var/lib/hades/transfer-intake"}' > site.json
```

Include existing quarantine stores too. Undeclared host directories, external
host service configuration and other independent services are **not** backed up.
The tool does not discover unknown installations or claim they are protected.

1. Schedule the acquisition outage and stop the host gateway, normalizer,
   historian adapter and transfer adapter. Observe their successful shutdown;
   volatile work can be lost at this boundary.
2. Run `hades-site -action backup -state /private/site-manager -archive /private/backups/site-a.tar`.
3. Record the returned SHA-256 in a separate trusted inventory, alongside the
   manifest/repository revision, versions, backup time and declared state paths.
4. Confirm the managed stack restarted, then restart compatible host adapters
   and check local health. A backup failure also attempts Compose restart and
   reports restart failure separately; never infer service recovery from the
   existence of an archive.

Backup refuses locked/missing declared host stores, checks live revision labels,
stops Compose, requires zero clean component exit status, measures cold volume
sizes, and checks temporary-copy/archive disk headroom plus a 128 MiB reserve.
It then copies cold broker/historian state, live broker policy, configuration and
host stores. One manager lock prevents concurrent deployment/backup.

The archive destination must be outside the manager and declared host store
trees, so capture cannot recursively include its own temporary files. Interrupting
the CLI process can leave services stopped; inspect and restart explicitly.

The archive is mode 0600 and is written exclusively, synced with its parent
folder, with an exact per-file checksum index. Limits are 10,000 files and 32 GiB
of source data. Symlinks, special files and private-key filenames are rejected.
This is a same-host consistency mechanism, not an encrypted/offsite backup
service. Send the private archive to separately approved encrypted storage and
retain its trusted digest outside the archive. Do not put it in Git.

## Restore safely

```sh
artifacts/hades-site -action restore -archive /private/backups/site-a.tar \
  -digest <separately-trusted-sha256> -target /private/restore/site-a
```

The target must not exist. Restore verifies the whole archive hash, rejects path
traversal/links/duplicates, enforces bounds and checks every file digest before
publishing the new directory. Files and directories are synced. It never
extracts over a running installation or imports into existing volumes.

To bring that staged restore online:

1. Select a **new isolated Compose project**, new host paths and nonconflicting
   ports. Use the same pinned component versions and storage formats. Rebuild
   the operational Go tools from the recorded source revision if needed.
2. Reprovision CA/server/client identities outside the restore tree. Verify
   certificate names, trust, validity, roles and protected endpoint access.
   Archived configuration contains old **references**, not the referenced keys.
3. Prepare/review a manifest using the restored release under
   `manager/releases/<digest>`, a new project name, new credential references,
   and `host_state` paths under the restored `host/` tree. Do not reuse the source
   manager marker or project identity as evidence of a running restore.
4. Create isolated destination volumes and containers, stop all destination
   services, and ensure their data directories are empty before import. Copy
   `broker/` to `/mosquitto/data` and `historian/` to `/storage`; preserve contents
   and set ownership to 1883:1883 and 10001:10001 respectively. Import
   `broker-policy.json` into the new prepared broker credentials and assign
   1883:1883 ownership. Keep new TLS keys separate from that policy restore.
5. Apply the new manifest, check live revision labels and protected endpoints,
   query known sample values/timestamps and verify retained broker messages.
   Open restored adapter stores with the preserved metadata revision, compare
   pending IDs/digests against the external source ledger, and reconcile remote
   receipt/dedup retention before enabling transfer replay.
6. Enable host adapters only after those checks and record elapsed recovery
   time, known recoverable IDs and any missing observations. Keep the source
   and restored stack isolated to avoid duplicate producers/consumer IDs.

`./scripts/smoke-site-recovery` executes this procedure on disposable Linux
volumes, including upgrade → invalid rejection → rollback, TLS trust replacement,
retained broker message, exact history sample and original queued identity.
Its test-only cleanup deletes **only** projects/volumes it created. Its fixture
replay callback verifies immutable queue bytes; it does not represent a deployed
Zeus acknowledgement. The real receipt boundary is tested by Hades-018/019.

## Evidence and limitations

Native macOS verification on 2026-10-15 restored retained broker state, the exact
67.2 sample/timestamp and one pending history/transfer identity with observed
loss 0 for those known records. Recovery from extraction through readiness and
identity verification measured **1,214 ms** for this tiny fixture, excluding
builds and operator planning. The native host has no Docker daemon, so that run
reports `rollout_upgrade_reject_rollback: false`; Linux CI supplies actual secure
Compose rollout/restore evidence. Reports live under ignored
`artifacts/site-recovery/report.json` and are printed in CI logs.

This measured time is not a production recovery target. Larger data, filesystem
behavior, operator credential provisioning and device restart can change it.
The physical-storage/power-loss boundary, full-volume capacity, PLC pilot and
production recovery objectives remain unqualified. There is no cross-service
transaction covering running producers; all writers must be quiesced as stated.

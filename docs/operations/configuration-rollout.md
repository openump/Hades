# Reviewed single-site configuration rollout

`hades-site` manages the broker/historian reference Compose stack (and its TLS
proxy in the secure profile). It does not reconcile a fleet, install host gateway
services, update PLC programs or claim the revision of independently started host
adapters. A selected real pilot remains pending.

## Prepare and review

```sh
go build -o artifacts/hades-site ./cmd/hades-site
artifacts/hades-site -action prepare -revision lab-a -project hades-lab \
  -ports '{"HADES_MQTT_PORT":"18893","HADES_VM_PORT":"18443"}' > site.json
artifacts/hades-site -action validate -manifest site.json -state runtime/site
```

The `openump-site/1` manifest pins SHA-256 for the exact profile files, four
contract schemas and approved metadata snapshot. `prepare` proposes a manifest;
review its source diff and hashes before `apply`. Validation checks strict JSON,
known fields, revision/project syntax, file hashes and safe non-symlink paths,
schema references, namespace/asset context, port references and external secret
references. Staging rereads hashes to reject changed source files. Unknown files
and inline secret content are not supported. Compose `config --quiet` runs on
the staged snapshot; it needs Docker Compose, but not a running daemon.

For the secure profile, first build the Linux proxy for the deployment host:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o artifacts/hades-history-proxy ./cmd/hades-history-proxy
artifacts/hades-site -action prepare -profile secure -revision secure-a \
  -project hades-secure-lab -security-dir /absolute/private/issued-credentials > secure-site.json
```

The credential directory must be mode 0700, with available CA/server certificate
pairs and private keys inaccessible to other users. Only its absolute reference
is staged. Keep PKI provisioning/rotation under the existing
[identity procedure](identity-networking.md); never commit keys or runtime bundles.

## Apply, observe and roll back

```sh
artifacts/hades-site -action apply -manifest site.json -state runtime/site
artifacts/hades-site -action status -state runtime/site
# After reviewing and applying lab-b, restore a previously recorded digest:
artifacts/hades-site -action rollback -digest <lab-a-manifest-sha256> -state runtime/site
```

A stable explicit Compose project preserves named volume identity across release
paths. The tool never runs `down -v`, renames projects, renews volumes or deletes
persistent data. It freezes releases in a private directory, invokes `up --build
-d --wait`, inspects actual running containers and compares both revision and
manifest-digest labels. Only then does a synced control record commit the active
revision. `status` checks those live labels again; a stored marker alone is not
proof of a running revision. Health for upstream services follows their Compose
profile; this is not an end-to-end host-adapter readiness probe.

Compose/Hades environment overrides and implicit `.env` loading are suppressed.
Manifest ports and secret references are the only Hades substitutions. Docker
connection/context settings still identify the operator-selected daemon.
There is one manager lock and at most 32 staged revisions. Preserve prior release
artifacts until the rollback/backup policy permits offline retention maintenance.

## Failure and compatibility boundaries

Apply is not a transaction across containers. A partial `up`, inspection failure
or final state-sync failure can leave a running or mixed revision while the old
active marker remains. The error states that uncertainty. Inspect container
labels and explicitly roll back the recorded digest; do not report success from
a marker or assume automatic rollback occurred. A crash during staging can leave
an incomplete release; quarantine that incomplete directory before retrying.

This first path preserves the profile, project identity, metadata bytes, Compose
volume topology, component artifacts and security-policy seed across upgrades. Changing those is rejected even if the new
snapshot is syntactically valid. Existing queues pin metadata context. Dynamic
Security's live policy persists in its credential volume; a new seed file does
not replace it, so seed-policy migrations are deliberately rejected. There is no
automatic database migration, historical metadata reconciliation or downgrade of
persistent data formats. Updates across different component persistence formats
need separate backup, compatibility evidence and operator planning.

Host adapters are outside this Compose lifecycle. Stop/restart them separately
with the reviewed snapshot and compatible credentials. Declare every persistent
adapter directory in the manifest's `host_state` map when making a site backup:
`history-intake`, `history-quarantine`, `transfer-intake`, `transfer-quarantine`,
and `normalization-quarantine`. Values are absolute paths. Validation rejects
unknown names and relative references; backup requires each declared store to
exist and be unlocked. Declaring no host stores means no host state is backed up.

## Verification and sources

`go test -race ./internal/site` covers rejected changes, immutable staging and
failed-apply ownership. `scripts/smoke-site-recovery` exercises live upgrade,
invalid rejection, rollback/history preservation and isolated restore in CI.
No live Docker daemon is available on the current development host; Compose
parsing is verified locally and actual container rollout is verified in Linux CI.

The behavior follows the official [Compose up volume-preserving recreation](https://docs.docker.com/reference/cli/docker/compose/up/),
[Compose config validation](https://docs.docker.com/reference/cli/docker/compose/config/),
and [container inspection](https://docs.docker.com/reference/cli/docker/inspect/)
interfaces. This is a reference deployment path, not pilot qualification.

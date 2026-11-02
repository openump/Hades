# The `hades` multicall binary and one-command bootstrap (Hades-041)

`cmd/hades` builds one binary containing every Hades role. Each standalone
`cmd/hades-<role>` is now a three-line wrapper around the same code in
`internal/cmds/<role>`, so the two are identical by construction.

| Invocation | Runs |
| --- | --- |
| `hades <role> [flags]` (e.g. `hades normalizer -listen …`) | the role, same flags, output and exit codes |
| a symlink named `hades-<role>` pointing at `hades` | the role (`argv[0]` dispatch) |
| `hades init`, `up`, `status`, `top`, `check -site-dir` | the bootstrap commands below |
| `hades roles` | the list of role names |

`hades-opcua-mock` stays a separate build-tagged test fixture.

A test runs each role's standalone binary, `hades <role>` and a symlink with
the same arguments (usage, flag errors, runtime errors) and requires identical
exit codes and output.

**Size** (linux/arm64, CGO-free, measured): all 18 standalone binaries total
200 MB, and six service binaries alone 78 MB; the one multicall binary that
replaces all 18 is 28.9 MB (build without `-s -w`; the image strips it further).

## Digest binding with one binary

Supervisor definitions (`hades-service`) pin `binary_sha256`. With the multicall,
a definition names the **resolved `hades` path** and passes the role as its first
argument:

```json
{ "binary": "/usr/local/bin/hades", "binary_sha256": "<sha256 of hades>",
  "args": ["history", "-broker", "mqtt://127.0.0.1:1883", "..."] }
```

The supervisor still refuses a symlinked `binary` (it hashes with `O_NOFOLLOW`),
so a definition can never be redirected through a link. One digest now covers
every role; replacing the binary changes it for all of them, which `hades check`
and the supervisor both detect. systemd units and scripts may keep using the
`hades-<role>` symlinks.

## `hades init`

```sh
hades init --profile demo        --out-dir my-site [--mqtt-port 1883 --historian-port 8428]
hades init --profile secure-site --out-dir my-site
```

Creates a private (0700) site directory:

- `site.json`: profile, contract root, the pinned binary, ports and services;
- `catalog/metadata.json`: the approved example metadata snapshot;
- `wal/`, `quarantine/`, `state/`, `logs/`;
- `services/<name>.json`: a digest-pinned supervisor definition per service;
- `pki/` (`secure-site`): scoped identities from `hades-pki`. These are **lab
  credentials**; `init` says so, and they must be replaced by the site CA before
  production.

`init` refuses a non-empty directory, builds the site in a temporary sibling directory
and renames it into place, so a failed init leaves nothing behind, and prints the next
command. Each site gets its own Compose project (`hades-<name>-<path hash>`).
`hades check --site-dir my-site` re-validates the catalog against the contracts and
every service definition against its pinned binary and file digests.

## `hades up`

`hades up --site-dir my-site` (or `hades up --demo`, which scaffolds `./hades-demo` if
needed) runs in the foreground:

1. re-verifies every pinned service definition (a changed binary refuses to start;
   rerun `init` after an upgrade);
2. starts the broker and historian with Docker Compose in a project named after the
   site, and waits until they are healthy (`--deps=false` uses already-running
   endpoints);
3. starts each service under its own `hades service -action run` supervisor with the
   definition digest pinned, and waits for supervisor-observed readiness;
4. for `demo`, runs one simulator that publishes an observation every `--interval`;
5. prints one JSON status line per event (`service_healthy`, `site_up`,
   `simulator_started`, …).

SIGINT or SIGTERM stops every supervisor and child (process groups) and then the
Compose dependencies (`--keep-deps` leaves them running; volumes are always kept).
`hades status --site-dir my-site` reports each service and exits non-zero unless all
are ready. Service output goes to `my-site/state/<service>/events.log`.

`up` brings up the `demo` profile end to end. For `secure-site`, start the secure
broker with `hades site` (or `deploy/compose.secure.yaml`) and run `up --deps=false`.

## `docker compose --profile demo`

Without a Go toolchain:

```sh
docker compose -f deploy/compose.yaml --profile demo up --build -d --wait
```

`deploy/Hades.Dockerfile` builds the multicall binary (CGO-free, `-trimpath`, pinned
base digests) into an unprivileged Alpine image with one symlink per role. The demo
services (historian, history, simulator, consumer) are that image running one role
each; Compose is the supervisor.

The demo containers **share the broker's network namespace**, so every cleartext MQTT
and HTTP hop is genuinely loopback, as the Hades-013 rule requires (cleartext only to
loopback); no rule was relaxed. The demo historian listens on loopback only, so other
containers on the `edge` network cannot reach it (the smoke test checks this), and it
has its own volume. It publishes no host port; query it with
`docker compose … --profile demo exec demo-historian wget -qO- http://127.0.0.1:8428/…`.
The broker still publishes only MQTT, so the plain development profile is unchanged.
`history` is healthy only after it prints `history_ready`, which follows its
acknowledged MQTT subscription.

## Scope

Evaluation and packaging ergonomics only. `hades up` and the demo image run the
anonymous loopback development profile; they do not weaken mTLS role separation
(Hades-013), binary digest verification (Hades-024/030) or the actuation gates
(Hades-033). The actuator and transfer are not part of the demo: they need a reviewed
actuation policy and an approved receiver.

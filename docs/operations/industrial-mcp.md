# Industrial MCP server (`hades-mcp`, Hades-034)

`hades-mcp` serves a Model Context Protocol endpoint so engineering copilots and
diagnostic agents can read plant context and propose setpoint changes. It is built on the
official MCP Go SDK. It contains **no fieldbus or PLC code**: its only write path is a
signed proposal to `hades-actuator`'s five-gate engine.

## Safety model

| Guarantee | How |
|---|---|
| A read-only agent cannot propose | Each role gets its own MCP server. The `mcp-readonly` server never registers `propose_command`, so the tool cannot be called and is not listed. |
| A session cannot be hijacked | Every MCP session is bound to the certificate identity that created it. A request presenting another identity's `Mcp-Session-Id`, or an ID the server never issued, gets 403. Without this, the SDK would route a read-only client holding a proposer's session ID to the proposer server. |
| An AI agent cannot cause an unconfirmed PLC write | `hades-mcp` caps every proposal at **L1**. `dry_run: true` gives L0 shadow evaluation; `false` gives L1 queued for explicit operator confirmation. L2/L3 are refused in `hades-mcp`, whatever the site policy allows. The actuator also never executes an unconfirmed L1, and in shadow mode it forces L0. |
| AI actions are attributable and opt-in per site | `hades-mcp` signs as its own origin (actor `hades-mcp`, its own Ed25519 key). Until a site adds that origin to an asset's `allowed_origins` in the actuation policy, the actuator rejects every MCP proposal. Receipts name the origin, so AI actions are distinguishable from operator actions. |
| An agent cannot weaken a gate | An agent chooses only the asset, the parameter, the value and `dry_run`. `hades-mcp` sets the command identity, origin, TTL and policy revision, and copies the policy's `required_preconditions` verbatim. |
| Invalid proposals never leave `hades-mcp` | Before publishing, it runs the actuator's own offline checks (`ValidateCommandContext`): schema, policy scope and revision, TTL, autonomy ceiling, origin allowlist, parameter allowlist, unit, safety envelope and required preconditions. |
| Results are genuine | A receipt is returned only if it is signed by a trusted actuator key and answers exactly this command: same `command_id` and same `command_sha256`. With no verified receipt, the tool reports the outcome as **unknown, not executed**. |

## Tools

All tools are rate-limited by a token bucket per caller identity (default 2/s, burst 10).

| Tool | Role | Source |
|---|---|---|
| `list_assets`, `get_asset_context` | read-only | approved catalog: ISA-95 hierarchy, measurements, units, ranges |
| `get_latest_telemetry` | read-only | read-only broker subscription, contract-validated and bound to the topic |
| `get_producer_status` | read-only | read-only broker `status` subscription |
| `query_metric_history` | read-only | `hades-history-proxy` with a query **built by `hades-mcp`** |
| `get_edge_health` | read-only | `hades-service` supervisor status (`-services`) |
| `propose_command` | proposer | `hades-actuator`, through signed MQTT `command/<parameter>` |

`hades-history-proxy` gates routes by identity but does not bound queries, so `hades-mcp`
enforces the bounds at the tool boundary, so every history source is covered, and never
forwards agent-supplied PromQL:
- exactly one declared series, selected by all four identifying labels
  (`asset_id`, `measurement`, `unit`, `metadata_revision`), so a unit change or revision
  bump cannot mix series;
- a window of at most 24 h;
- a step of at least 1 s;
- at most 1440 returned points (a range query returns window/step + 1);
- label values restricted and quoted, so a matcher cannot be injected.

The value metric holds good-quality readings only, so a gap means bad or uncertain data,
not zero.

## Transports and roles

- **stdio** (local agent): the role is `-stdio-role`, default `mcp-readonly`. Proposing
  needs an explicit `-stdio-role mcp-proposer`.
- **HTTPS** (`-listen`, loopback only, mTLS required): the verified client certificate CN
  selects the role from `-readonly-clients` and `-proposer-clients`. Unlisted or
  unverified clients get **403** and never reach the MCP layer. A CN listed under both
  roles is refused at startup.

Proposing requires `-policy` (the reviewed actuation policy), `-signing-key` (the
`hades-mcp` origin's private key) and `-actuator-keys` (receipt verification keys).

`-proposal-ttl` (default 60 s, the command schema maximum) is the proposal lease. The
actuator checks the lease again when an operator confirms an L1 proposal, so the operator
must confirm within it. A shorter lease makes confirmation impractical.

## Broker and history grants

The `hades-mcp` identity:
- **may** read `telemetry`, `status` and `command_receipt/+`, and publish `command/+`, for
  its asset;
- **may not** publish telemetry, receipts or events, or reach other assets.

`hades-history-proxy` grants `hades-mcp` exactly one route, `GET /api/v1/query_range`.
Export, instant queries and POST are refused, so the `hades-mcp` certificate cannot be
used to bypass these bounds by calling the proxy directly. `scripts/smoke-security`
proves the broker denials on a real broker.

Each `hades-mcp` process uses a unique MQTT client ID. Several instances, such as one
stdio process per agent, do not take over each other's session. A failed resubscribe
after a reconnect is logged as `mcp_degraded`.

## Deployment note: actuator client IDs

`hades-actuator` derives its MQTT client ID from its receipt key ID
(`hades-actuator-<key-id>`) and uses a persistent session. Two actuators with the same
receipt key ID on one broker share a session and can take each other's commands. Give
every actuator a distinct receipt key ID.

## Verification

`cmd/hades-mcp` runs the deployed, supervised `hades-actuator` in shadow mode on a real
mTLS broker. A `hades-mcp` proposal travels the production path and returns a
verified, signed receipt (`SHADOW_LOGGED`). Tests also show that a read-only caller cannot
list or call `propose_command`, that out-of-envelope and non-allowlisted proposals are
refused offline, and that the L1 cap holds even under an L3 policy.

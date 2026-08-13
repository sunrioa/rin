# MCP and Host Control Quick Start

[English](mcp-control-plane.md) | [简体中文](mcp-control-plane.zh-CN.md)

Rin separates external control into two local processes:

```text
Codex / Claude / MCP Client
        | STDIO
        v
     rin-mcp  --------\
     rin-mcp  --------+--> rin-control :7375 <--> game Host
     rin-mcp  --------/
```

- `rin-control` is the long-lived daemon. It exclusively owns the listen port,
  Operation state directory, bearer token, and fixed trusted Principal.
- `rin-mcp` is a stateless STDIO proxy that maps MCP tools to the daemon client
  API.
- Multiple MCP clients may each start a proxy. Exiting one proxy does not stop
  the daemon or game Host.
- The game Host uses the same daemon to publish world state, receive requests,
  and report authoritative Outcomes.

The current build uses the official Go MCP SDK `v1.7.0-pre.3`. SDK negotiation
prefers `2026-07-28`. Both this SDK and protocol version remain Preview; Rin CI
runs the official conformance scenarios that match Rin's exposed capabilities.

## Build

Go 1.25 or later is required:

```bash
go build -o bin/rin ./cmd/rin
go build -o bin/rin-control ./cmd/rin-control
go build -o bin/rin-mcp ./cmd/rin-mcp
```

Create a local token, configure one Principal already recognized by the game,
and start the daemon:

```bash
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read,skill.read"
export RIN_CONTROL_DATA_DIR="/absolute/path/to/rin-control-data"

./bin/rin-control
```

The token must contain at least 32 bytes. `rin-control` listens on
`127.0.0.1:7375` by default and rejects non-loopback addresses. Use an absolute
data-directory path in persistent configurations.

An Actor's published `owner_principal_id` must match the configured Principal or
the Actor is hidden from ordinary clients. `host.admin` can read across owners
and should not be granted by default.

## One-command MCP Client Setup

`rin` detects Codex, Claude Code, and OpenClaw CLIs and invokes each client's
official MCP management commands. The first run lists detected Agents; select
numbers, names, or press Enter for all:

Command shapes follow the official [Codex MCP](https://developers.openai.com/codex/mcp),
[Claude Code MCP](https://code.claude.com/docs/en/mcp), and
[OpenClaw MCP](https://docs.openclaw.ai/cli/mcp) references.

```bash
export RIN_CONTROL_TOKEN="replace-with-the-same-random-secret"
./bin/rin mcp install
```

For noninteractive setup, select clients explicitly or accept every detected
client:

```bash
./bin/rin mcp install -agents codex,claude,openclaw
./bin/rin mcp install -yes
```

The installer:

1. atomically installs the sibling `rin-mcp` binary at a stable path below the
   operating system's user configuration directory;
2. writes the loopback Control URL and token once to a private
   `mcp-client.json` file (mode `0600` on Unix);
3. uses the official Agent CLI to register
   `rin-mcp --config <private-file>`, so Agent configurations contain no token;
4. records registrations it owns in a manifest and rolls ownership back when
   a failed CLI command is confirmed to have written nothing.

With `--config`, the token and URL in that private file are not overridden by
stale environment variables accidentally inherited by an Agent process. Only
an explicit `--control-url` argument temporarily overrides its URL. Without a
config file, the existing `RIN_CONTROL_URL` and `RIN_CONTROL_TOKEN` behavior is
unchanged.

An existing `rin` MCP server not owned by this installer is never overwritten
by default. Use `-force` only to take it over, or `-repair` to rewrite an owned
registration that was changed manually. Restart or reload selected Agent
clients after installation.

Inspect all Agent registrations, the managed binary, and private config without
printing the token:

```bash
./bin/rin mcp status
```

Missing Codex, Claude Code, or OpenClaw commands are reported as undetected and
do not affect another client.

### One-command update

Run the command from an unpacked newer Rin distribution:

```bash
./bin/rin mcp update
```

It uses the new `rin-mcp` beside the current `rin` executable and atomically
replaces the stable managed binary without rewriting Agent registrations or
the private connection config. An identical SHA-256 is reused without another
replacement. A verified binary may also be supplied
explicitly:

```bash
rin mcp update -server /absolute/path/to/new/rin-mcp
```

Windows locks running executable images, so exit Agents using Rin MCP before
updating. Restart Agents after an update on macOS or Linux as well so existing
STDIO sessions load the new version.

This repository does not currently promise an automated binary release
pipeline with published SHA-256 manifests, so the command does not silently
download an unverified executable. A future verified network updater can retain
this command surface.

### Uninstall

By default, remove all manifest-owned Agent registrations while keeping the
managed files for easy reinstall, or select clients explicitly:

```bash
rin mcp uninstall
rin mcp uninstall -agents codex,claude
```

Remove the managed binary, manifest, and private connection config as well:

```bash
rin mcp uninstall -purge
```

The uninstaller only removes registrations it owns and exact managed regular
files. It neither deletes an unmanaged same-name entry nor follows symlinks.

## Manual MCP Client Configuration

When a supported Agent CLI is unavailable, configure the MCP client to start
`rin-mcp` manually. Environment variables remain supported:

```json
{
  "mcpServers": {
    "rin": {
      "command": "/absolute/path/to/rin/bin/rin-mcp",
      "env": {
        "RIN_CONTROL_URL": "http://127.0.0.1:7375",
        "RIN_CONTROL_TOKEN": "replace-with-the-same-random-secret"
      }
    }
  }
}
```

The equivalent command using the installer's central config is:

```bash
/absolute/path/to/rin-mcp --config /absolute/path/to/mcp-client.json
```

Only the `rin-control` startup configuration selects the Principal and scopes.
Tool arguments and proxy processes cannot elevate them. Proxy standard output is
reserved for MCP wire traffic; diagnostics go to standard error.

`rin-control` also owns the shared Skill Catalog. It loads standard `SKILL.md`
documents from `skills/installed` and `skills/learned` below the data directory,
so the internal Agent and every MCP proxy see the same catalog. Skills are inert
guidance and cannot grant a capability or bypass policy.

## Tools

`actor.read` registers these read-only tools:

| Tool | Purpose |
| --- | --- |
| `list_worlds` | List worlds visible to the fixed Principal |
| `list_actors` | List visible Actors in one world |
| `get_actor_state` | Read the Host's redacted Actor, controller, and emergency-stop summary |
| `wait_actor_update` | Long-poll observation, authority, controller, and stop cursors for up to 25 seconds |
| `observe_actor` | Read the complete Host-authored V2 Observation and opaque references |
| `list_actor_capabilities` | List compact summaries of currently published typed capabilities |
| `describe_actor_capability` | Read one capability's input, output, and effect schemas |

Every authorized MCP client can also use `get_operation` and `wait_operation`.
Daemon scopes register the remaining tools:

| Tool | Minimum scope | Purpose |
| --- | --- | --- |
| `acquire_actor_control` | `actor.control` | Acquire the sole epoch-bound deliberative controller lease |
| `renew_actor_control` | `actor.control` | Renew the same controller lease |
| `release_actor_control` | `actor.control` | Release control and fence unfinished work |
| `get_actor_control` | `actor.control` | Read the current live controller |
| `confirm_action` | `actor.control` | Confirm one policy challenge after a fresh Host snapshot check |
| `set_actor_emergency_stop` | `actor.control` | Let the Actor owner or Host administrator change the safety latch |
| `submit_actor_action` | `actor.execute` | Submit arguments matching an exact Host capability schema |
| `cancel_operation` | `operation.cancel` | Request cancellation; this is not rollback |
| `list_skills` | `skill.read` | List summaries compatible with an adapter and available capabilities |
| `get_skill` | `skill.read` | Read one exact version of a complete skill |
| `save_experience_as_skill` | `skill.write` | Save verified procedural guidance as a learned `SKILL.md` |
| `reload_skills` | `skill.write` | Reload installed and learned skills from disk |

For an external Agent that may observe, control, and execute registered Host
capabilities:

```bash
export RIN_CONTROL_SCOPES="actor.read,actor.control,actor.execute,operation.cancel,skill.read"
```

Add `skill.write` only when an external Agent may save experience. It can modify
learned guidance but grants no game action. The tool description requires
success claims to be supported by authoritative Outcomes; automatic extraction
and review are implemented in a later stage.

Restart `rin-control` after changing scopes. Policies that confirm high-risk
effects configure a separate challenge TTL for each Host clock:

```json
{
  "confirmation_ttl": {
    "event": 16,
    "step": 600,
    "realtime": 30000
  }
}
```

A zero or omitted value disables confirmation for that clock and returns the
auditable `policy.confirmation_clock_disabled` denial instead of an internal
error. A challenge never outlives its BoundAction `valid_until`.

The standard loop is:

1. Read the current Epoch and observation sequence through `get_actor_state` and
   `observe_actor`.
2. Discover exact typed actions with `list_actor_capabilities` and
   `describe_actor_capability`.
3. Acquire a short exclusive lease through `acquire_actor_control`, renewing it
   while continuous control is required.
4. Call `submit_actor_action` with the exact capability version, digest,
   observation cursor, and opaque Host references.
5. Call `confirm_action` only when policy requires it, then use
   `wait_operation` for the authoritative result.

MCP cannot submit a Principal, scope, effect preview, risk level, or policy
decision. Its arguments express intent. The Host resolves opaque references,
normalizes arguments, and binds real effects on the authority thread before Rin
policy allows, denies, or requests confirmation. Dialogue, gathering,
navigation, and building are all ordinary game-registered capabilities. A
macro may create auditable children with `parent_operation_id` only after the
Host accepts it. The parent must be published as `kind=macro` with
`produces_child_operations=true`, and parent and child must carry the same
non-empty `task_id`. Planning never bypasses binding, policy, or final
authorization.

The direct result of every write tool means only that Rin accepted or queued the
request. It is not evidence that the game executed it. Callers copy `cursor`
unchanged into `wait_operation`, or continue using `get_operation`:

- `queued`, `delivered`, `accepted`, and `running` never mean completed;
- report NPC execution as successful only when `execution_confirmed=true`, which
  requires `status=succeeded` and an authoritative Host `outcome`;
- `terminal=true` means the state is settled and cannot receive a different
  authoritative result. Never relabel `failed`, `rejected`, `cancelled`,
  `interrupted`, or `stale` as success;
- `reconciliation_pending=true` means the current state is `outcome-unknown`
  and the Host may still submit an authoritative Outcome. Continue using
  `wait_operation` and do not treat it as final success or failure;
- `stale` with `delivery_attempts=0` means the Host never received the request;
- `wait_operation.changed=false` only means no newer revision arrived during the
  wait and supplies no execution evidence;
- Host state is opaque. Attribute observations by explicit `subject` and
  `subject_id`; a nested context for another subject cannot prove Actor behavior.
  Actor execution requires an Operation Outcome or Actor-owned task telemetry.

An `outcome-unknown` without an authoritative Outcome waits for Host
reconciliation from its Pending Journal and Outcome Outbox for a bounded
retention period, then becomes eligible for pruning. A Host-reported
`outcome-unknown` with an Outcome is a settled uncertain result.

## Character Decision Authority

A Host may publish `decision_authority` for an Actor:

- With `source=internal`, ordinary external clients may only observe. A Host
  administrator is the only external Principal allowed to acquire that lease.
- With `source=external`, only a daemon Principal exactly matching
  `controller_principal_id` may acquire the ordinary controller lease.
- `persona_mode=character-bound` asks the external agent to portray the
  Host-defined character.
- `persona_mode=agent-avatar` lets the external agent use its own personality
  and private memory while embodying the Actor.
- Every handoff increases `revision`. Unaccepted Operations from an older
  revision become stale; an already accepted bounded action may finish.

Authority selects who makes the next semantic decision. Navigation, combat, and
building remain per-tick Host controllers. Rin neither turns model output into
frame-by-frame movement nor copies either controller's private memory.

## Host Lifecycle

The game Host uses the same `RIN_CONTROL_URL` and token:

1. `register` obtains a Lease.
2. `publish` atomically replaces the World, Actor, complete Observation, and
   Capability Snapshot.
3. `poll` long-polls for binding/snapshot gateway requests, authorized
   Operations, and cancellation.
4. `gateway-result` returns a Host-created BoundAction or fresh authority
   snapshot.
5. The Host revalidates the BoundAction on its authority thread, then uses
   `ack` to accept or reject the stable Operation ID.
6. Optional `run` calls report monotonic progress; `outcome` reports the sole
   authoritative terminal result and strict JSON output.
7. The Host periodically calls `renew` and calls `unregister` on shutdown.

Final authorization and every world mutation must run on the game-owned thread.
MCP, internal AI, and in-game commands should all call the same game execution
service so entry points cannot acquire different permission semantics.

The Control contract is
[`api/control-openapi.json`](../api/control-openapi.json). Host routes are:

| Method and path | Purpose |
| --- | --- |
| `GET /control/v2/health` | Unauthenticated liveness check |
| `POST /control/v2/host/register` | Register a Host and acquire a Lease |
| `POST /control/v2/host/renew` | Renew the Lease |
| `POST /control/v2/host/unregister` | Go offline explicitly |
| `POST /control/v2/host/publish` | Publish one World read model |
| `POST /control/v2/host/poll` | Receive gateway requests, Operations, and cancellation |
| `POST /control/v2/host/gateway-result` | Return an action binding or authority snapshot |
| `POST /control/v2/host/ack` | Accept or reject a delivery |
| `POST /control/v2/host/run` | Report action progress |
| `POST /control/v2/host/outcome` | Report the authoritative result and output |

The `/control/v2/*` client routes used by `rin-mcp` and language SDKs cover
discovery, controller leases, actions, Operations, and emergency stop. Client
request bodies never carry a Principal; the daemon always injects its fixed
startup Principal to prevent identity spoofing. Hosts use
`/control/v2/host/*`.

Error responses always contain a human-readable `error` and may include a stable
machine-readable `code`. Current service codes are `invalid`, `forbidden`,
`not_found`, `lease_expired`, `unavailable`, `lease_conflict`, `stale`,
`not_accepted`, `conflict`, `capacity`, and `internal`. Clients should branch on `code` when it
is present and treat the HTTP status as the compatibility fallback.

## Persistence and Recovery

`operations.json` in `RIN_CONTROL_DATA_DIR` uses mode 0600, strict JSON,
temporary-file synchronization, and atomic replacement. A cross-platform process
lock permits only one `rin-control` writer. The file never stores the token,
model keys, prompts, or a game save. It accepts only the Action-only
`rin.control.operations/v5` schema and does not load older request formats.

Recovery rules:

- newly queued requests, ACKs, cancellation, and Outcomes are immediately durable;
- delivery counters and ActionRun progress are checkpoints folded into the next
  durable write or graceful shutdown;
- a crash before ACK makes the original binding unsafe to reuse, so the Operation
  restores as `stale` and the controller must submit again from a fresh
  Observation;
- accepted work with no reported run or Outcome is redelivered by the same
  Operation ID so the Host can resume from its durable Pending Journal;
- accepted work with reported execution but no Outcome restores as
  `outcome-unknown`; the Host reconciles it from its Outcome Outbox;
- a persisted `stale` or unresolved `outcome-unknown` state is never revived as
  queued or accepted work after restart;
- requests bound to a stale Epoch or Observation never
  reach a new timeline;
- unfinished work without a Host expires to `stale` or `outcome-unknown` instead
  of occupying capacity forever;
- Host Leases and read models remain Host-owned and must be republished after
  reconnect.

## Security Boundary

- Both `rin-control` and `rin-mcp` accept loopback endpoints only.
- Every non-health request requires the same bearer token of at least 32 bytes.
- Never put the token in a repository, prompt, MCP argument, game save, or log.
- Reusing the same Principal and `request_id` returns the same Operation; changing
  the payload conflicts.
- The Host must process redelivered Operation IDs idempotently.
- `output` must not contain API keys, complete prompts, unredacted world state, or
  engine objects.
- Remote MCP, dynamic pairing, multiple Principals, and simultaneous write
  controllers are not currently supported. They are explicit
  [roadmap](../ROADMAP.en.md) items.

`rin-mcp -conformance-addr` exists only for local official conformance tests and
requires a daemon Principal with exactly `actor.read`. It is not a production
Streamable HTTP deployment entry point.

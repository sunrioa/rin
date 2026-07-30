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
go build -o bin/rin-control ./cmd/rin-control
go build -o bin/rin-mcp ./cmd/rin-mcp
```

Create a local token, configure one Principal already recognized by the game,
and start the daemon:

```bash
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read"
export RIN_CONTROL_DATA_DIR="/absolute/path/to/rin-control-data"

./bin/rin-control
```

The token must contain at least 32 bytes. `rin-control` listens on
`127.0.0.1:7375` by default and rejects non-loopback addresses. Use an absolute
data-directory path in persistent configurations.

An Actor's published `owner_principal_id` must match the configured Principal or
the Actor is hidden from ordinary clients. `host.admin` can read across owners
and should not be granted by default.

## MCP Client Configuration

Keep `rin-control` running, then configure the MCP client to start `rin-mcp`:

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

Only the `rin-control` startup configuration selects the Principal and scopes.
Tool arguments and proxy processes cannot elevate them. Proxy standard output is
reserved for MCP wire traffic; diagnostics go to standard error.

## Tools

`actor.read` registers four read-only tools:

| Tool | Purpose |
| --- | --- |
| `list_worlds` | List worlds visible to the fixed Principal |
| `list_actors` | List visible Actors in one world |
| `get_actor_state` | Read the Host's redacted Actor publication |
| `list_actor_offers` | Read exact Offers from an online Host |

Daemon scopes may also register:

| Tool | Minimum scope | Purpose |
| --- | --- | --- |
| `send_actor_message` | `actor.converse` | Send dialogue without authorizing a world mutation |
| `send_actor_directive` | `actor.direct` | Submit a goal the Actor or Host may reject |
| `execute_actor_offer` | `actor.execute` | Select one complete current Offer |
| `get_operation` | any control scope | Inspect delivery, run, and Outcome state |
| `cancel_operation` | `operation.cancel` | Request cancellation; this is not rollback |

For conversation and exact actions, for example:

```bash
export RIN_CONTROL_SCOPES="actor.read,actor.converse,actor.execute,operation.cancel"
```

Restart `rin-control` after changing scopes. `execute_actor_offer` does not accept
arbitrary action parameters, coordinates, item IDs, or method names. The
Operation stores the complete Host Offer and Epoch/Observation binding. The game
still revalidates the Offer, deadline, permissions, and current world state on
its authoritative thread.

## Host Lifecycle

The game Host uses the same `RIN_CONTROL_URL` and token:

1. `register` obtains a Lease.
2. `publish` atomically replaces the World, Actor, and current Offer read model.
3. `poll` long-polls for messages, directives, exact offers, and cancellation.
4. `ack` accepts or rejects a stable Operation ID.
5. Optional `run` calls report monotonic progress.
6. `outcome` reports one authoritative terminal result and up to 64 KiB of strict
   JSON `output`.
7. The Host periodically calls `renew` and calls `unregister` on shutdown.

Final authorization and every world mutation must run on the game-owned thread.
MCP, internal AI, and in-game commands should all call the same game execution
service so entry points cannot acquire different permission semantics.

The Control contract is
[`api/control-openapi.json`](../api/control-openapi.json). Host routes are:

| Method and path | Purpose |
| --- | --- |
| `GET /control/v1/health` | Unauthenticated liveness check |
| `POST /control/v1/register` | Register a Host and acquire a Lease |
| `POST /control/v1/renew` | Renew the Lease |
| `POST /control/v1/unregister` | Go offline explicitly |
| `POST /control/v1/publish` | Publish one World read model |
| `POST /control/v1/poll` | Receive work and cancellation |
| `POST /control/v1/ack` | Accept or reject a delivery |
| `POST /control/v1/run` | Report action progress |
| `POST /control/v1/outcome` | Report the authoritative result and output |

`/control/v1/client/*` routes are used by the typed `rin-mcp` HTTP client. Client
request bodies never carry a Principal; the daemon always injects its fixed
startup Principal to prevent identity spoofing.

## Persistence and Recovery

`operations.json` in `RIN_CONTROL_DATA_DIR` uses mode 0600, strict JSON,
temporary-file synchronization, and atomic replacement. A cross-platform process
lock permits only one `rin-control` writer. The file never stores the token,
model keys, prompts, or a game save.

Recovery rules:

- newly queued requests, ACKs, cancellation, and Outcomes are immediately durable;
- delivery counters and ActionRun progress are checkpoints folded into the next
  durable write or graceful shutdown;
- a crash before ACK safely redelivers the same Operation ID, although the
  delivery counter may reset;
- accepted work without an Outcome restores as `outcome-unknown`; the Host
  reconciles it from its Pending Journal and Outcome Outbox;
- requests bound to an old Epoch, old Observation, or legacy unbound state never
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

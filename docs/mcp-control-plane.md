# MCP Gateway Quick Start

[English](mcp-control-plane.md) | [简体中文](mcp-control-plane.zh-CN.md)

`rin-mcp` is Rin's optional external-control gateway. The current release:

- it uses the official Go MCP SDK `v1.7.0-pre.3`;
- it uses the SDK's default version negotiation, preferring `2026-07-28` while
  remaining compatible with legacy revisions supported by the SDK;
- MCP clients connect over STDIO;
- game hosts publish state through token-authenticated loopback HTTP;
- MCP and Host traffic share one in-process Control Plane;
- defaults to read-only and registers message, directive, exact-offer, and
  cancellation tools only for explicitly granted scopes;
- uses stable operation IDs, idempotent `request_id` values, Host
  acknowledgements, progress, and outcomes for writes;
- recovers Operations from an atomic local state file that never contains the
  bearer token.

Pairing management and Streamable HTTP MCP are future phases described in the
[implementation plan](mcp-control-plane-plan.md).

## Build

Go 1.25 or later is required:

```bash
go build -o bin/rin-mcp ./cmd/rin-mcp
```

Create a dedicated token and select a game-authorized principal:

```bash
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read"
export RIN_CONTROL_DATA_DIR="/absolute/path/to/rin-control-data"
```

The token must contain at least 32 bytes. Do not put it in the repository, MCP
arguments, prompts, game saves, or logs. An Actor's published
`owner_principal_id` must match this principal or the Actor remains invisible
to the MCP client.

## MCP client configuration

Configure the binary as a local STDIO server. Client-specific configuration
file locations differ, but the server entry follows this shape:

```json
{
  "mcpServers": {
    "rin": {
      "command": "/absolute/path/to/rin/bin/rin-mcp",
      "env": {
        "RIN_CONTROL_TOKEN": "replace-with-a-random-secret",
        "RIN_CONTROL_PRINCIPAL": "player.one",
        "RIN_CONTROL_SCOPES": "actor.read",
        "RIN_CONTROL_DATA_DIR": "/absolute/path/to/rin-control-data"
      }
    }
  }
}
```

Standard output is reserved for the MCP wire. Diagnostics go to standard error.

Protocol negotiation is delegated entirely to the official SDK. Modern clients
use `server/discover` and per-request `_meta`; legacy clients can use the
legacy `initialize` handshake directly. Rin does not pin or emulate a protocol
revision.

## Available tools

| Tool | Purpose |
| --- | --- |
| `list_worlds` | List worlds visible to the configured principal |
| `list_actors` | List visible Actors in one world |
| `get_actor_state` | Read redacted Actor state published by the Host |
| `list_actor_offers` | Read exact current Action Offers from an online Host |

All four tools are read-only and idempotent. A disconnected Host's retained
state remains readable, while its Offers become unavailable. `host.admin` can
read every principal and should be granted only by explicit trusted local
configuration.

Explicit grants register these additional tools:

| Tool | Minimum scope | Purpose |
| --- | --- | --- |
| `send_actor_message` | `actor.converse` | Send conversation without directly authorizing world mutation |
| `send_actor_directive` | `actor.direct` | Submit a goal that the Actor and Host may refuse |
| `execute_actor_offer` | `actor.execute` | Select one exact current `offer_id` |
| `get_operation` | any Control scope | Read delivery, progress, Outcome, and structured Host output |
| `cancel_operation` | `operation.cancel` | Request cancellation without implying rollback |

For example:

```bash
export RIN_CONTROL_SCOPES="actor.read,actor.direct,actor.execute,operation.cancel"
```

`execute_actor_offer` accepts no action arguments, coordinates, item IDs, or
arbitrary method names. It binds the complete Offer most recently published by
the Host. Immediately before authority-thread dispatch, the Host still checks
the Epoch, descriptor, deadline, principal, and game rules. Trusted startup
configuration may also include game-specific Capability scopes for final Host
authorization.

## Recovery

`RIN_CONTROL_DATA_DIR` defaults to `./rin-control-data` under the current
working directory. MCP clients may choose different working directories, so a
production configuration should use an absolute path. `operations.json` uses
0600 permissions, strict JSON, and an fsynced temporary file followed by an
atomic replacement. The file is limited to 64 MiB; new requests use only a
32 MiB budget so progress and outcomes retain headroom.

The state file contains the messages, directives, invocations, principal
scopes, and outcomes needed for delivery and reconciliation. A Host may attach
one strict JSON object of at most 64 KiB as terminal `output`. The state file
does not contain the bearer token or model API keys. Only one `rin-mcp` process
may write the directory.

After restart:

- unacknowledged requests return to `queued` with the same Operation ID;
- acknowledged work without an Outcome becomes `outcome-unknown`;
- the Host reconciles from its durable pending-work and Outcome Outbox;
- Host Leases and Read Models are not persisted by Rin and must be registered
  and published again;
- persisted terminal Operations remain queryable by their original principal.

## Host publication endpoints

`rin-mcp` listens on `127.0.0.1:7375` by default and rejects non-loopback listen
addresses. Every `/control/v1/*` request requires:

```http
Authorization: Bearer <RIN_CONTROL_TOKEN>
Content-Type: application/json
```

Current endpoints:

| Method and path | Purpose |
| --- | --- |
| `GET /health` | Unauthenticated liveness check |
| `POST /control/v1/register` | Register a Host and acquire a Lease |
| `POST /control/v1/renew` | Renew the Lease |
| `POST /control/v1/publish` | Atomically publish one World Read Model |
| `POST /control/v1/poll` | Long-poll for requests and cancellation notices |
| `POST /control/v1/ack` | Accept or reject a delivered request |
| `POST /control/v1/run` | Report monotonic progress |
| `POST /control/v1/outcome` | Report the terminal result, bounded evidence, and optional structured `output` |
| `POST /control/v1/unregister` | Go offline while retaining the last Read Model |

Request bodies are limited to 1 MiB by default. Unknown fields and duplicate
JSON properties are rejected. Hosts follow
`register -> publish -> renew -> unregister`. World mutation remains on the
game's authority thread; the Control Plane neither stores nor resolves engine
objects.

`output` is an optional JSON object beside `outcome` in the
`/control/v1/outcome` request. It can carry dialogue, a selected Offer, or
another Host-defined result. Rin validates strict JSON, object shape, and the
64 KiB limit, persists it, and returns it as structured data from
`get_operation`. It must not contain API keys, full prompts, unredacted world
state, or engine objects.

## Authority boundary

- `actor.read` can read only Actors whose `owner_principal_id` matches the
  configured principal.
- `host.admin` can read all Actors and is disabled by default.
- Startup configuration grants scopes. Model output and MCP tool arguments
  cannot elevate authority.
- Retrying the same `request_id` for the same principal returns one Operation;
  changing the payload creates a conflict.
- Unacknowledged requests may be redelivered with the same Operation ID and
  must be handled idempotently by the Host.
- Lease expiry makes unaccepted work `stale`; accepted work without an Outcome
  becomes `outcome-unknown`.
- Disconnecting the MCP client cannot mutate the game world.

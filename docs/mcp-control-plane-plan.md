# MCP External Control and Host Control Plane Plan

[English] | [Simplified Chinese](mcp-control-plane-plan.zh-CN.md)

Status: planned, not yet a supported Rin `0.7.0` capability.

This document defines how Codex, Claude Code, and other MCP clients can safely
control in-game actors while sharing the same authoritative execution path as
in-game commands and AI. Game-specific world models remain in their adapters.

## Goals and non-goals

The implementation will provide game-neutral actor discovery, conversation,
directives, exact offer execution, and operation management. MCP and in-game
APIs will share permissions, epoch validation, game-thread execution, save
data, and outcomes. Local stdio and loopback Streamable HTTP are supported,
while NPCs retain the ability to refuse or negotiate directives.

The MCP server will not expose shell, console, arbitrary scripts, reflection,
or unrestricted game APIs. Clients cannot author coordinates, object
references, item IDs, NBT, capability names, or unbound arguments. The first
version does not include a public multi-tenant relay, cross-server migration,
or a way to bypass NPC boundaries, server policy, or player confirmation.

## Required implementation order

1. Host Control Plane contract and read model.
2. Read-only MCP gateway.
3. Bounded directives and exact offer execution.
4. One end-to-end real host adapter.
5. Broader gameplay capabilities, long tasks, and multi-actor coordination.

Rin currently primarily supports `Host -> Rin -> Policy`. External control
requires a safe `MCP -> Rin -> Host` reverse channel. Connecting MCP tools
directly to one mod would duplicate authentication, queuing, idempotency, and
outcome recovery in every engine.

The first playable slice lists worlds and actors, reads redacted actor state
and offers, sends a message or rejectable directive, executes one still-valid
host-authored `offer_id`, and polls the operation to a terminal state.

## Target architecture

```text
Codex / Claude Code / other MCP clients
                |
       stdio or Streamable HTTP
                v
          cmd/rin-mcp
                |
       authenticated local API
                v
       Rin Host Control Plane
       - host lease and heartbeat
       - actor/read model registry
       - bounded command queue
       - operation/event journal
       - scope and audit policy
                ^
       host poll / ack / report
                |
Minecraft / Ren'Py / Godot / Unity / custom engine adapter
                |
       authoritative game thread
```

The host registers and long-polls for work. Games do not expose another inbound
port, MCP clients never hold engine objects, and all world mutation stays on the
authoritative game thread.

## Same-effect invariants

Requests from game UI, chat commands, in-game AI, and MCP must:

1. enter the same host `ControlService` or equivalent application service;
2. use the same trusted principal, scopes, and ownership state;
3. use the same current epoch, offer, descriptor, and deadline;
4. perform the same final TOCTOU checks;
5. resolve host references and mutate the world only on the authoritative thread;
6. use the same operation ID, applied marker, save data, and outcome outbox;
7. emit the same world events, NPC-memory inputs, and audit result;
8. return a structured failure rather than silently performing another action.

Same effect means that the same valid input reaches the same executor and
authoritative result. It does not mean different models choose the same action,
or that an MCP client receives administrator authority.

## Control Plane contract

| Object | Purpose |
| --- | --- |
| `HostRegistration` | Declares the host, protocol, manifest, worlds, and control support |
| `HostLease` | Time-bounded connection ownership that gates new writes |
| `ActorSnapshot` | Redacted state, semantic location, tasks, and visible relationships |
| `OfferSnapshot` | Current host-authorized offers with epoch, digest, and deadline |
| `ControlRequest` | Message, directive, exact offer execution, or cancellation |
| `ControlOperation` | Retryable, queryable queued and execution state |
| `ControlEvent` | Monotonic progress, outcome, and invalidation events |
| `ControlOutcome` | Host-observed terminal result with bounded evidence |

All IDs are opaque. Principal, scopes, host ID, and world ID come from pairing
and host registration, never from caller-authored tool arguments.

```text
submitted -> queued -> delivered -> accepted -> running -> succeeded
                        |            |          |-> failed
                        |            |          |-> cancelled
                        |            |          |-> interrupted
                        |            |          `-> outcome-unknown
                        |            `-> rejected
                        `-> stale
```

`request_id` provides retry idempotency. A host persists request acceptance
before acknowledging delivery, and Rin persists terminal outcomes before
removing pending reports. Lease expiry makes undelivered requests stale;
delivered work requires reconciliation. Cancellation is a request, not rollback.

Exact paths and JSON shapes will be defined in OpenAPI 3.1. The API covers host
registration and leases, world/actor/snapshot/offer publication, bounded
long-poll delivery, acknowledgments, progress, outcomes, restart
reconciliation, principal-filtered queries, and immediate write blocking after
pairing or scope revocation.

## MCP gateway

The first version exposes a small stable tool set instead of generating one MCP
tool for every gameplay capability:

| Tool | Minimum scope | Meaning |
| --- | --- | --- |
| `list_worlds` | `actor.read` | List visible online worlds |
| `list_actors` | `actor.read` | List visible actors in a world |
| `get_actor_state` | `actor.read` | Read a redacted snapshot and active operations |
| `list_actor_offers` | `actor.read` | Read current unexpired offers |
| `send_actor_message` | `actor.converse` | Converse without direct world effects |
| `send_actor_directive` | `actor.direct` | Submit a rejectable or negotiable objective |
| `execute_actor_offer` | `actor.execute` | Select an exact current `offer_id` |
| `get_operation` | matching action scope | Read status and structured result |
| `cancel_operation` | matching action scope | Request cancellation |

`send_actor_directive` is the default write surface. `execute_actor_offer` is a
stronger permission, but accepts only an exact, currently published bound offer.

The MCP wire supports only `2026-07-28` and does not negotiate down to an older
protocol. Pin the official Go MCP SDK `v1.7.0-pre.3` until a stable release with
0728 support passes regression. Both stdio and Streamable HTTP use
`server/discover` and standard per-request `_meta`; HTTP uses the 0728 stateless
mode, binds only to loopback, validates `Origin`, requires a random bearer
token, and limits body size, concurrency, and idle duration. The MCP SDK stays
in `mcpbridge` and `cmd/rin-mcp`; Rin Core and `host` do not import it.

Recommended scopes are `actor.read`, `actor.converse`, `actor.direct`,
`actor.execute`, `operation.cancel`, and `host.admin`. Pairing binds a client to
allowed hosts, worlds, actors, scopes, and expiry. High-risk offers may require
in-game confirmation bound to operation and epoch. Audit stores opaque IDs,
tool, scope, state, time, and latency, not prompts, secrets, full dialogue, or
save payloads.

## Package boundaries

```text
controlplane/       domain objects, state machines, queue ports, policy
controlplane/store/ persistence and recovery
mcpbridge/          MCP tool to Control Plane conversion
cmd/rin-mcp/        optional MCP server binary
sdk/*/control/      host control clients
```

Existing `runtime`, `host`, and `hostkit` responsibilities remain unchanged.
The Control Plane reuses epoch, offer, invocation, run, and outcome contracts
without copying or weakening their validation.

## Delivery phases

### R0: contract, threat model, and fixtures

- Define objects, limits, state transitions, protocol negotiation,
  principal/scopes, leases, idempotency, and audit rules.
- Add an in-memory fake host and cross-language JSON fixtures.
- Test invalid IDs, limits, duplicate properties, unknown fields, wrong epochs,
  expired leases, idempotency, schema fuzzing, and state-machine fuzzing.

### R1: read-only Control Plane

- Add host registration, leases, heartbeat, recovery, world/actor publication,
  snapshots, offers, persistent read models, and query APIs.
- Test restart, lease expiry, actor removal, monotonic snapshots, redaction,
  pagination, principal isolation, concurrency, and races.

### R2: read-only MCP

- Add `cmd/rin-mcp`, stdio, and the four read tools.
- Add Codex and Claude Code configuration examples and compatibility tests.
- Verify that MCP cannot mutate the world, client exit does not affect the host,
  and an offline host is reported explicitly.

### R3: write queue and operations

- Add message, directive, exact-offer execution, host long polling,
  acknowledgment, progress, outcomes, cancellation, idempotency, bounded
  backpressure, scopes, pairing, revocation, and high-risk confirmation.
- Test lost responses, duplicate delivery, Rin and host restart, late outcomes,
  full queues, cancellation races, unknown outcomes, stale offers, changed
  descriptors/epochs, and revoked scopes.

### R4: first real-host vertical slice

- Route in-game commands and MCP through one control service.
- Validate reads, conversation, directives, exact offers, and long tasks.
- Execute every world effect on the game thread.
- Reconcile operations, save data, and Rin reports after restart.

Do not add a large dynamic tool catalog or claim stable generic write control
until this phase passes.

### R5: cross-language Host Control SDKs

Add minimal Java, C#, Python, JavaScript, and C/Lua clients according to real
host demand. Each covers registration, renewal, snapshot/offer publication,
request polling, acknowledgment, outcome reporting, local pending work, and
conformance fixtures. SDKs never bypass engine threading or authorization.

### R6: events and multi-actor coordination

- Add bounded operation event cursors or MCP notifications.
- Add shared objectives, roles, conflict arbitration, per-actor knowledge and
  permissions, host-owned resource locks, fair scheduling, and independently
  auditable operations in batch work.

### R7: hardening and Preview release

- Validate stdio and loopback HTTP on macOS, Windows, and Linux.
- Manually interoperate with Codex, Claude Code, and one additional MCP client.
- Run reconnect, queue-pressure, fault-injection, and storage-corruption tests.
- Verify redaction, token rotation, licenses, SBOM, OpenAPI/SDK inventory, and
  Chinese/English documentation consistency.

## Test matrix and completion criteria

Tests cover schema and fuzzing; client/principal/world/actor/scope isolation;
same-executor consistency; epoch, deadline, lease, TOCTOU, and late-result
ordering; retry and restart idempotency; queue and outbox pressure; loopback,
Origin, token, and redaction security; and cross-client/OS/SDK compatibility.

Screen lock does not block unit, protocol, headless, GameTest, build, or static
checks. Human confirmation UX, visuals, and long play sessions remain explicit
manual acceptance items.

The first MCP control release is complete only when one real game proves both
entry points share an execution service, scopes can be independently granted
and revoked, no arbitrary execution surface exists, restart cannot duplicate
effects, long tasks are observable and cancellable when supported, MCP failure
does not stop the game, and the public documentation is bilingual.

## Commit convention

Implementation uses Conventional Commit types with Chinese descriptions:

```text
docs: 添加 MCP 控制面实施计划
feat: 增加 Host 租约与 Actor 快照
feat: 增加只读 MCP 工具
fix: 修复重复投递导致的操作重放
refactor: 统一游戏内与 MCP 控制入口
test: 增加 Host 重启恢复测试
```

Each phase receives at least one independent commit. Protocol, generated files,
and tests remain consistent in the same phase commit.

# Signal Inbox

[English](signals.md) | [简体中文](signals.zh-CN.md)

A Signal is a short-lived Host hint that something may deserve an actor's
attention. It is not an action, fact write, permission, or execution result.
Adapters own namespaced kinds such as `minecraft.player.death`.

## Flow

1. The adapter merges noisy game events and publishes the latest actor Observation.
2. The Host publishes a Signal under the same Lease, Epoch, and Observation Sequence.
3. Rin applies ID deduplication, per-kind cooldown, expiry, and bounded capacity.
4. Under internal authority, an enabled Persona with a matching Initiative trigger may wake and create a normal task.
5. Under external authority, Rin never calls the internal model; the external Agent reads `list_actor_signals` or `wait_actor_signals` through MCP.
6. Any resulting action still traverses Controller, Policy, Operation, and authoritative Host Outcome.

Signals are disabled by default. A Host configures `enabled`,
`cooldown_millis`, and `max_pending` per actor. Inboxes are process-local and
are discarded on daemon restart so old-Epoch reminders cannot enter a new timeline.

## Host API

[`../api/signal-openapi.json`](../api/signal-openapi.json) is authoritative:

- `POST /signals/v1/host/settings`
- `POST /signals/v1/host/publish`
- `POST /signals/v1/list`
- `POST /signals/v1/wait`

Java adapters can use `HostControlSession.configureSignals` and
`HostControlSession.publishSignal`. Host input must match the actor's current
Epoch and Observation; Rin assigns `received_at_unix_millis` and `cursor`.

## Boundaries

- Adapters collect and merge events; Rin Core owns no cross-game emotion dictionary.
- A summary states an observation or an explicitly uncertain hypothesis, never an authoritative Outcome.
- Disabled, duplicate, cooled-down, or capacity-limited Signals return a PublishResult reason and create no task.
- Signals are not durable or acknowledged and add no `claim/ack` state machine.

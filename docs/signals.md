# Signal Inbox

[English](signals.md) | [简体中文](signals.zh-CN.md)

A Signal is a short-lived Host hint that something may deserve an actor's
attention. It is not an action, fact write, permission, or execution result.
Adapters own namespaced kinds such as `minecraft.player.death`.

## Flow

1. The adapter merges noisy game events and publishes the latest actor Observation.
2. The Host publishes a Signal under the same Lease, Epoch, and Observation Sequence.
3. Rin applies ID deduplication, per-kind cooldown, expiry, and bounded capacity.
4. Under internal authority, an enabled Persona with a matching Initiative trigger routes the event through its Actor's current task before creating new work.
5. Under external authority, Rin never calls the internal model; the external Agent reads `list_actor_signals` or `wait_actor_signals` through MCP.
6. Any resulting action still traverses Controller, Policy, Operation, and authoritative Host Outcome.

Signals are disabled by default. A Host configures `enabled`,
`cooldown_millis`, and `max_pending` per actor. Inboxes are process-local and
are discarded on daemon restart so old-Epoch reminders cannot enter a new timeline.

## Actor coordination

- An unfinished task receives bounded untrusted Signal context. An observation
  wait wakes immediately; operation waits and manual pauses retain their gates.
- Same-kind context merges to its newest summary. A task retains up to 8 pending
  kinds and 64 recent Signal IDs for durable deduplication. Context is consumed
  after a successful model decision; expired or different-epoch context is excluded.
- An idle Actor may start one initiative task. Task creation is serialized by
  Host/World/Actor, including concurrent callers using the same controller ID.
  The Persona's `cooldown_millis` also limits new ordinary initiative tasks.
- `initiative_policy.preempt_triggers` is an explicit kind allowlist, empty by
  default. A matching urgent Signal may request cancellation of an ordinary
  initiative task, then waits for its Host operations to settle before starting
  a replacement. Caller-created tasks and other urgent initiative tasks only
  receive context. An unresolved `outcome-unknown` blocks automatic replacement.
- An older accepted observation sequence can trigger a fresh decision in the same
  epoch. The runtime always collects current Host evidence; an old Signal never
  supplies a reusable action binding. A different epoch is discarded.

The scheduler records `delivery.status` (`started`, `attached`, `merged`, `retry`
or `dropped`), reason, task ID, attempt count and retry time on the inbox record.
Transient failures retry after 1, 2, 4, then 8 seconds, at most 32 attempts and
never beyond expiry. New Signals continue while another Signal waits. Disabling
the inbox drops pending delivery. Re-list from cursor zero to inspect changed
delivery diagnostics; ordinary cursor waits track newly published Signals.

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
- Inbox delivery diagnostics remain process-local and expire with the Signal;
  pending context already attached to a Task is durable. No public `claim/ack`
  authority is added, and Hosts cannot supply `delivery` state.

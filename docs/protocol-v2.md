# Rin Protocol v2

[English] | [简体中文](protocol-v2.zh-CN.md)

Rin `0.7.0` is a **Preview** release. Its wire identifier is
`rin.protocol/v2`. v2 is intentionally breaking: a v1 request is rejected
rather than interpreted through a compatibility path.

The authoritative machine-readable contract is
[`api/openapi.json`](../api/openapi.json). This document explains the
cross-engine rules that JSON Schema alone cannot express.

## Authority boundary

Rin proposes; the game host decides and executes.

- The host creates observations, epochs, decision windows, action offers, and
  stable operation identities.
- A policy may select only one complete offer authored for that exact decision
  window. It cannot invent a capability, target, or argument.
- An accepted proposal is still advice. Only the authoritative host can start
  or complete an operation.
- The host reports the actual decision and lifecycle through
  `/v2/action/report` or `/v2/action/report-batch`.
- Rin never executes shell commands, game console commands, generated code, or
  arbitrary model-supplied method names.

## Transport and envelopes

Every JSON request except health probes includes:

```json
{"protocol_version":"rin.protocol/v2"}
```

Successful responses use `{"ok":true,"data":...}`. Errors use
`{"ok":false,"error":{"code":"...","message":"...","field":"..."}}`.
Clients must reject redirects, bound request and response sizes, require HTTPS
for non-loopback endpoints, and keep bearer tokens outside game saves.

Identifiers use lowercase ASCII segments separated by `.`, `_`, or `-`.
Integers carried across SDKs must be in the JSON-safe range
`0..9007199254740991`. JSON object member names must be unique at every wire
and persistence boundary.

## Session creation

`POST /v2/session/create` binds one durable playthrough to immutable content:

```json
{
  "protocol_version": "rin.protocol/v2",
  "request_id": "create.playthrough-1",
  "session_id": "playthrough-1",
  "binding": {
    "game_id": "example.game",
    "content_id": "base",
    "content_version": "1.0.0",
    "content_hash": "content-build-42"
  },
  "features": [],
  "actors": [{
    "id": "npc.mira",
    "kind": "npc",
    "display_name": "Mira",
    "think_every_ticks": 5,
    "enabled": true
  }]
}
```

No feature opt-in is required for the v2 action lifecycle. Optional features
remain additive capabilities and must be negotiated through `/health`.
Repeating the exact request is idempotent; reusing its `request_id` with a
different payload is an error.

## Epochs and host time

An `Epoch` prevents work captured for one world generation from being applied
to another:

```json
{
  "session_id": "playthrough-1",
  "world_id": "overworld",
  "host": 1,
  "world": 4,
  "timeline": 2
}
```

- Increment `host` when authority is recreated.
- Increment `world` after a scene, level, shard, or authoritative world load.
- Increment `timeline` after rollback, save-line fork, or loading older state.

All three generations are positive JSON-safe integers. The Epoch
`session_id` must equal the containing request and Session state; a nested
Epoch from another Session is rejected even when all other fields are valid.

`Timepoint` is `{ "clock": "event|step|realtime", "value": N }`.
Realtime values are Unix milliseconds; event and step values are monotonic
host counters. Render frames are not authoritative time.

## Observation

`POST /v2/session/observe` records a host-observed event. Each observation
includes its `epoch` and a positive, monotonic `observation_seq`. Large images,
audio, telemetry, and replay slices should be stored externally and referenced
as immutable artifacts rather than embedded in the event log.

An optional `payload` is a `HostValidatedPayload`, not untrusted model output.
Its schema reference is an assertion by the authenticated Host: the adapter
must validate `data` against that exact schema and digest before sending the
request. Rin validates the reference, byte limit, and strict JSON envelope but
does not resolve game-owned schemas. Go adapters should construct it with
`protocol.NewHostValidatedPayload`; other adapters must enforce the equivalent
local check.

## Decision window and offers

A proposal request binds one actor to one host-owned opportunity:

```json
{
  "protocol_version": "rin.protocol/v2",
  "session_id": "playthrough-1",
  "request_id": "propose.turn-42",
  "actor_id": "npc.mira",
  "tick": 42,
  "intent": "Respond to the player.",
  "decision_window": {
    "id": "window.turn-42",
    "mode": "sequential",
    "epoch": {
      "session_id": "playthrough-1",
      "world_id": "overworld",
      "host": 1,
      "world": 4,
      "timeline": 2
    },
    "observation_seq": 81,
    "opened_at": {"clock":"step","value":420},
    "deadline": {"clock":"step","value":430},
    "actor_ids": ["npc.mira"]
  },
  "offers": [{
    "offer_id": "offer.greet",
    "decision_window_id": "window.turn-42",
    "actor_id": "npc.mira",
    "capability": {"id":"dialogue.say","version":"1.0.0"},
    "descriptor_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "description": "Say the authored greeting.",
    "arguments": {"text":"Welcome, traveler."},
    "expected_epoch": {
      "session_id": "playthrough-1",
      "world_id": "overworld",
      "host": 1,
      "world": 4,
      "timeline": 2
    },
    "observation_seq": 81,
    "deadline": {"clock":"step","value":430}
  }]
}
```

An offer is a fully bound authorization envelope. `descriptor_digest` identifies
the exact validated capability descriptor. Targets are opaque `HostRef` values
resolved only by their owning adapter. A proposal is valid only while the epoch,
observation sequence, deadline, actor, window, digest, and selected offer still
match authoritative game state.

Decision modes are `sequential`, `simultaneous`, and `asynchronous`.
Simultaneous results that mutate shared state should be arbitrated and reported
as one batch.

## Action lifecycle

The host assigns a stable `operation_id` and reports one of:

- `rejected`: no invocation, run, outcome, facts, or goal updates;
- `accepted` with `invocation` and a `queued` or `running` run;
- `accepted` with a terminal run and matching outcome.

Run statuses are `queued`, `running`, `succeeded`, `failed`, `cancelled`,
`interrupted`, `stale`, and `outcome-unknown`. A terminal report must contain an
outcome with the same operation, status, and expected epoch.

```json
{
  "protocol_version": "rin.protocol/v2",
  "session_id": "playthrough-1",
  "request_id": "report.operation-42",
  "tick": 43,
  "report": {
    "proposal_id": "proposal.turn-42",
    "event_id": "action.operation-42",
    "decision": "accepted",
    "summary": "Mira greeted the player.",
    "invocation": {
      "operation_id": "operation-42",
      "offer_id": "offer.greet",
      "decision_window_id": "window.turn-42",
      "actor_id": "npc.mira",
      "capability": {"id":"dialogue.say","version":"1.0.0"},
      "descriptor_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "arguments": {"text":"Welcome, traveler."},
      "expected_epoch": {
        "session_id": "playthrough-1",
        "world_id": "overworld",
        "host": 1,
        "world": 4,
        "timeline": 2
      },
      "observation_seq": 81,
      "deadline": {"clock":"step","value":430}
    },
    "run": {
      "operation_id": "operation-42",
      "status": "succeeded",
      "progress_seq": 1,
      "progress": 100,
      "updated_at": {"clock":"step","value":421}
    },
    "outcome": {
      "operation_id": "operation-42",
      "status": "succeeded",
      "summary": "The line was displayed.",
      "epoch": {
        "session_id": "playthrough-1",
        "world_id": "overworld",
        "host": 1,
        "world": 4,
        "timeline": 2
      },
      "world_seq": 82,
      "occurred_at": {"clock":"step","value":421}
    }
  }
}
```

Reports describe effects that already happened. Replaying an acknowledged
report must never apply the game action again.

## Durable integration algorithm

A world-mutating host should persist:

1. one complete Pending Turn before its first network request;
2. any returned Job ID before polling it;
3. an operation marker and exact report in one game transaction where
   transactional durability is claimed;
4. an Outcome Outbox until Rin acknowledges the exact report.

After restart, drain the outbox first, then resume the saved Pending Turn with
the exact same request identity and payload. An idempotent host may retry
`Execute(operation_id)`; an advisory host must not claim that this closes the
crash window.

Proposal Jobs are process-local optimization. If a saved Job is absent after a
sidecar restart, resubmit the exact durable proposal request. Do not create a
new request identity.

## Storage and long-running sessions

Use `/v2/session/stats` to monitor event-log, snapshot, checkpoint, and index
bytes. Configure soft and hard per-session limits, snapshot periodically, and
archive completed lineages. Session export/import uses bounded NDJSON streams
for lineages larger than the inline snapshot limit.

Rin keeps bounded actor details and derived indexes; the append-only event
history remains the source of reconstruction. Identifier History rejects
cross-kind reuse even after compacted entities leave current state.

## Compatibility rule

The only supported wire contract in this release is `rin.protocol/v2`.
Additive response fields may appear during Preview, so SDKs should ignore
unknown response fields. Requests remain strict: unknown request fields are
rejected to expose integration mistakes early.

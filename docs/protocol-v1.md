# Rin Protocol v1

[English](protocol-v1.md) | [简体中文](protocol-v1.zh-CN.md)

This reference defines the stable HTTP and state contract between Rin and a
game-owned adapter.

## Envelope

Requests use `Content-Type: application/json`. The default maximum body is
32 MiB so a complete save snapshot can fit; individual fields and arrays have
smaller structural limits. A successful response is:

```json
{"ok":true,"data":{}}
```

An error response is:

```json
{
  "ok": false,
  "error": {
    "code": "invalid_request",
    "message": "must be between 1 and 5",
    "field": "importance"
  }
}
```

Except for bodyless job query and cancellation endpoints, every JSON request
body must contain:

```json
{"protocol_version":"rin.protocol/v1"}
```

IDs are 1 to 96 characters and may contain only letters, digits, `.`, `_`,
and `-`. This prevents path traversal at the source and remains compatible
with Windows file names.

## Create session

`POST /v1/session/create`

```json
{
  "protocol_version": "rin.protocol/v1",
  "request_id": "create.playthrough-1",
  "session_id": "playthrough-1",
  "binding": {
    "game_id": "my-game",
    "content_id": "base-story",
    "content_version": "1.0.0",
    "content_hash": "sha256:..."
  },
  "seed": 42,
  "features": ["memory-archive-v1", "belief-conflicts-v1"],
  "actors": [
    {
      "id": "npc.mira",
      "kind": "npc",
      "display_name": "Mira",
      "traits": ["curious", "careful"],
      "boundaries": [
        {
          "id": "boundary.privacy",
          "description": "Do not reveal private records.",
          "trigger_tags": ["private"],
          "response": "refuse"
        }
      ],
      "goals": [
        {
          "id": "goal.connect",
          "description": "Build trust through specific actions.",
          "priority": 4,
          "preferred_actions": ["talk"],
          "progress": 0,
          "target_progress": 3,
          "status": "active"
        }
      ],
      "think_every_ticks": 5,
      "enabled": true
    }
  ]
}
```

The binding prevents state from another story or mod version from being
silently restored into the current game.

`features` contains compatibility switches explicitly selected for a new
session. `/health` returns the supported values:

- `memory-archive-v1`: compress memories outside the detailed window into
  deterministic hierarchical summaries;
- `belief-conflicts-v1`: retain actor-private conflicting claims and their
  sources;
- `goal-candidates-v1`: allow a policy to propose one bounded subgoal supplied
  by the current request;
- `actor-activity-v1`: enable region and awake/dormant lifecycle;
- `arbitration-v1`: enable world revision, multi-actor arbitration, and atomic
  batch commit.

Legacy sessions that omit this field keep v0.4 behavior, including replay
hashes and JSON shape.

## Observe

`POST /v1/session/observe`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "observe.event-18",
  "event_id": "event-18",
  "tick": 18,
  "observer_ids": ["npc.mira"],
  "source": "game",
  "kind": "dialogue",
  "summary": "The player waited instead of demanding an answer.",
  "quote": "Take your time.",
  "tags": ["conversation", "trust"],
  "importance": 4,
  "facts": [
    {
      "subject_id": "player",
      "predicate": "respected_boundary",
      "object": "event-18",
      "visibility": ["npc.mira"],
      "confidence": 100
    }
  ]
}
```

Only actors in `observer_ids` receive the memory. If a fact has a
`visibility` list, it is written only to observers on that list, preventing
NPCs from learning events they did not perceive.

## Propose

`POST /v1/agent/propose`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "propose.turn-19.mira",
  "actor_id": "npc.mira",
  "tick": 19,
  "intent": "Choose how to respond.",
  "tags": ["conversation"],
  "candidate_actions": [
    {"id":"talk","kind":"dialogue","description":"ask one honest question"},
    {"id":"refuse","kind":"refuse","description":"protect a private boundary"},
    {"id":"wait","kind":"wait","description":"stay silent for now"}
  ],
  "candidate_goals": [
    {
      "id": "goal.ask-about-photo",
      "description": "Find a calm moment to ask about the old photograph.",
      "priority": 2,
      "progress": 0,
      "target_progress": 2,
      "status": "active"
    }
  ]
}
```

The returned proposal includes:

- `based_on_revision` and `based_on_head_hash`: state used to generate it;
- `action`: copied from the game's candidate actions; the policy cannot grant
  new authority;
- `recalled_memory_ids` and `goal_id`: auditable evidence;
- `rationale`: one character-facing sentence for UI, not hidden model
  reasoning;
- `status: pending`: the proposal has no effect until committed;
- `policy_source`: `model`, `model-cache`, `boundary-guard`,
  `deterministic-fallback`, or an offline source.

Policy execution does not hold the session lock. If a new observation arrives
first, the call returns `state_changed`; retry with a new `request_id`.

Candidate goals require `goal-candidates-v1` and are limited to eight. A
policy cannot invent a goal; it may select an existing goal or one supplied by
this request. A candidate goal travels with the proposal and enters actor
state only after acceptance. Rejection or staleness leaves no goal behind.

Games using an online model should not call this synchronous endpoint from
their main thread. Use the asynchronous job API.

## Async proposal jobs

Submission uses the same body as Propose:

`POST /v1/jobs/propose`

The service immediately returns `202 Accepted`:

```json
{
  "ok": true,
  "data": {
    "protocol_version": "rin.protocol/v1",
    "job_id": "job....",
    "status": "queued",
    "duplicate": false
  }
}
```

Query requires no body:

`GET /v1/jobs/{job_id}`

Status is `queued`, `running`, `succeeded`, `failed`, `stale`, or `canceled`.
On success, `proposal` contains a normal ActionProposal. Failure returns only
a safe error code, never a provider response body.

Cancel with:

`DELETE /v1/jobs/{job_id}`

Repeated submissions with the same session and `request_id` return the same
job. A different payload returns `request_id_conflict`. The queue is bounded;
when full it returns `429 jobs_queue_full`.

## Structured generation jobs

Structured generation is for constrained dialogue, scenes, quest text, or
ending presentation. It neither reads nor modifies sessions, creates no world
facts, and cannot replace the Proposal/Commit authority boundary.

`POST /v1/generation/jobs`

```json
{
  "protocol_version": "rin.protocol/v1",
  "request_id": "generation.scene-12",
  "kind": "scene",
  "context_hash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "messages": [
    {"role":"system","content":"Return one bounded scene JSON object."},
    {"role":"user","content":"{\"storylet_id\":\"scene-12\"}"}
  ],
  "temperature": 0.6,
  "max_tokens": 1024,
  "response_format": "json_object"
}
```

Allowed `kind` values are `director`, `story`, `scene`, `decision`, `ending`,
`free-response`, and `storylet-selection`. There must be 1 to 8 messages, with
per-message and total character limits. `context_hash` is a caller-generated
SHA-256 identifier for semantic context, diagnostics, and consistency checks.

Submission immediately returns `202 Accepted`. Query and cancel:

```text
GET    /v1/generation/jobs/{job_id}
DELETE /v1/generation/jobs/{job_id}
```

Status is `queued`, `running`, `succeeded`, `failed`, or `canceled`. A
successful result contains the raw JSON object plus bounded metadata such as
model name, finish reason, token usage, and `cache_hit`. Rin parses output
again; arrays, plain text, empty content, invalid UTF-8, NUL, and oversized
content fail.

The same `request_id` and payload return the same job. Semantically identical
requests with different IDs may hit the short-lived cache. Generation jobs do
not enter the event log. A game must validate the result against its own
content contract before accepting it into canon. Provider failure never
generates replacement story automatically; callers must supply offline
content.

## Commit

`POST /v1/action/commit`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "commit.turn-19.mira",
  "proposal_id": "proposal....",
  "event_id": "event-19",
  "tick": 19,
  "accepted": true,
  "outcome": "Mira asked what the player wanted remembered.",
  "tags": ["conversation"],
  "facts": [],
  "goal_updates": []
}
```

Accepting a proposal records the action outcome, updates scheduling, marks
recalled memories, and advances the associated goal by one. Rejecting a
proposal does not modify actor memories, facts, or goals.

## Living-world coordination

With `actor-activity-v1`, the game calls this endpoint when regions load,
unload, or change simulation level:

`POST /v1/session/activity`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "activity.school-day-2",
  "tick": 80,
  "updates": [
    {"actor_id":"npc.mira","region_id":"school.roof","state":"awake"},
    {"actor_id":"npc.teacher","region_id":"school.office","state":"dormant"}
  ]
}
```

`state` is either `awake` or `dormant`. Dormant actors do not appear in the
scheduler and cannot propose. `/v1/scheduler/due` accepts an optional
`region_ids` filter.

With `arbitration-v1`, several actors may produce proposals at the same world
revision before calling `POST /v1/world/arbitrate`:

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "arbitrate.turn-81",
  "tick": 81,
  "proposal_ids": ["proposal.mira", "proposal.teacher"],
  "exclusive_target_ids": ["prop.camera-1"]
}
```

Results are deterministically ordered by target priority, tick, actor ID, and
proposal ID, then marked `selected` or `deferred`. Arbitration records a
recommendation and never changes the game world directly. After applying
selected actions, the game may use `POST /v1/action/commit-batch` to commit at
most one result per actor. If any entry is stale or invalid, the entire batch
is rejected without partial mutation.

## Scheduler

`POST /v1/scheduler/due`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "tick": 24,
  "limit": 16,
  "region_ids": ["school.roof"]
}
```

Results are stably sorted by `next_think_tick` and actor ID for turn-based,
regional, and time-sliced games.

## Snapshot and restore

Snapshot and Session State requests use the same shape:

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1"}
```

Restore:

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "restore.save-slot-2",
  "snapshot": {"protocol_version":"rin.protocol/v1","state_hash":"...","state":{}}
}
```

Restore rejects snapshots with an invalid hash, different session ID, or
different binding, and clears pending proposals.

When a game repeatedly loads the same save, the restore `request_id` should
bind both the target snapshot hash and the sidecar's current head hash. A
network retry remains idempotent, while loading the old save again from a
later state creates a new restore event and performs a real rollback.

## Timeline and replay

`POST /v1/session/timeline` returns paginated event type, revision, hash,
request ID, actor/entity IDs, and status. It never returns observation
summary/quote, commit outcome, prompt, or model body:

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1","after_revision":0,"limit":50}
```

Use `next_after_revision` for the next page. `limit` is 1 to 256.

`POST /v1/session/replay` runs the normal reducer and hash-chain verification
to rebuild a selected revision, then returns an in-memory snapshot:

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1","revision":42}
```

Replay includes actor memories and story state present at that revision, so
it keeps the Session API authentication boundary and is not a redacted log
endpoint.

## Common errors

| HTTP | Code | Meaning |
| --- | --- | --- |
| `400` | `invalid_json` / `invalid_request` | JSON or field-contract error |
| `401` | `unauthorized` | Missing or incorrect Bearer token |
| `404` | `session_not_found` / `unknown_actor` | Entity does not exist |
| `404` | `revision_not_found` | Replay revision does not exist |
| `409` | `state_changed` / `proposal_stale` | Base state changed |
| `409` | `actor_not_due` | Actor has not reached its thinking tick |
| `422` | `no_safe_action` | Boundary triggered without a safe candidate |
| `413` | `body_too_large` | Request exceeds the body limit |
| `429` | `jobs_queue_full` / `jobs_capacity` | Proposal queue or retention is full |
| `429` | `generation_queue_full` / `generation_capacity` | Generation queue or retention is full |
| `503` | `jobs_unavailable` / `jobs_closed` | Proposal jobs are disabled or closing |
| `503` | `generation_unavailable` / `generation_closed` | Generation is disabled or closing |

The service never places event payloads, tokens, internal paths, or raw model
responses in error messages.

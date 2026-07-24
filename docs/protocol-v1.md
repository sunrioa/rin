# Rin Protocol v1

[English](protocol-v1.md) | [简体中文](protocol-v1.zh-CN.md)

This reference defines the stable HTTP and state contract between Rin and a
game-owned adapter.

## Envelope

Requests use `Content-Type: application/json`. The default maximum request body
is 32 MiB; individual fields and arrays have smaller structural limits.
Every bundled client also defaults to a 32 MiB maximum response body. Inline
Snapshot compact JSON has a separate 16 MiB ceiling, leaving transport headroom
for the response envelope, Restore metadata, and durable EventRecord framing.
Rin returns `413 snapshot_too_large` rather than truncating any Snapshot.
Identifier History grows with a Session lineage, so a lineage that exceeds the
inline ceiling cannot use the Snapshot or Replay JSON transport. No streaming
Snapshot transport is currently provided.
A successful response is:

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

## Durable request and event identities

Every mutation that durably changes a Session carries a caller-generated
`request_id`. Within one Session lineage, including restarts, Restore
generations, and branches later abandoned by Restore, Rin permanently binds
that ID to:

- the mutation kind;
- a SHA-256 digest of the complete request after strict decoding into its typed
  protocol value and canonical JSON encoding; and
- the first durable operation result.

Object member order and insignificant JSON whitespace therefore do not change
the identity; array order and every typed field do. An exact retry returns the
first result without another mutation. Mutation responses retain the first
event's `revision` and `head_hash`; Proposal and Arbitration return their
original typed result. The retry response alone sets `duplicate=true`.
These revision/head fields acknowledge the first operation and are not the
Session's current head; call `/v1/session/get` when the current State is needed.
Reusing a `request_id` for another kind or payload returns
`409 request_id_conflict`.

Observe, Commit, and every Commit Batch item share one permanent
`(session_id, event_id)` namespace. Once an Event ID has been accepted anywhere
in that lineage, another request using it returns `409 event_exists`. Event ID
is not a second idempotency key: only an exact retry with the original
`request_id` returns a duplicate success. Different Sessions may use the same
ID, although globally unique caller IDs are recommended for save portability.

The bounded `state.receipts` map is a hot compatibility and diagnostic
projection, not the authoritative idempotency index. Evicting a Receipt,
Proposal, Arbitration, Memory, Summary, or other State projection never makes
its request or Event ID reusable.

Proposal Job and Generation Job records use separate bounded, in-process
retention rules described below. They are not themselves durable Session
mutations.

If an append or initial Create/fresh-Restore write fails and Rin cannot prove
whether the event became durable, a non-Proposal endpoint returns
`mutation_outcome_unknown`. This is an unresolved outcome, not a confirmed
failure or success. The caller must retain the operation and retry its exact
mutation kind, typed payload, `request_id`, and any Event IDs. An altered
same-ID retry returns `409 request_id_conflict`; any other mutation for that
Session is blocked with `409 mutation_outcome_unknown` until the exact retry
confirms the tail. The operation that first exposes the storage uncertainty
normally returns HTTP `500` and may do so again while confirmation remains
unavailable. A successful recovery can be a normal or duplicate response
depending on which durable evidence Rin confirms; in either case, it never
applies the logical mutation twice.

Proposal appends retain the compatibility code
`proposal_outcome_unknown` and the same fail-closed exact-retry rule. Never
replace either unresolved request with a new ID or let an offline fallback
overtake it.

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
  "features": ["outcome-reporting-v1", "memory-archive-v1", "belief-conflicts-v1"],
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
  batch commit;
- `outcome-reporting-v1`: make the game the sole outcome authority, allow late
  reports, and merge Facts, Goals, memories, actions, and scheduling by game
  occurrence time.

Legacy sessions that omit a feature keep the corresponding historical reducer
behavior and replay result. In particular, `outcome-reporting-v1` is never
enabled automatically for an existing event log. Feature-enabled returned
state may include optional occurrence metadata; tolerant JSON decoders must
ignore fields they do not recognize.

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

With `outcome-reporting-v1`, Rin stamps each returned Fact with the enclosing
request tick as `observed_tick`; callers do not set that field in requests
(omitted or zero is accepted). An authoritative Observation may then arrive
after the Session tick has advanced, including save/restore reconciliation.
Rin preserves the original `tick`, orders memory by occurrence time, and
prevents older Facts from replacing newer values. Sessions without the Feature
keep the legacy monotonic-tick and arrival-order behavior and do not populate
`observed_tick`.

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

Every candidate `ActionSpec.description` is a game authorization boundary: the
game must supply text that is safe to show to the player if that action is
selected. Do not place private goal, boundary, memory, belief, or prompt text
in an action description.

The returned proposal includes:

- `based_on_revision` and `based_on_head_hash`: state used to generate it;
- `action`: copied from the game's candidate actions; the policy cannot grant
  new authority. Only its `description` is display-authorized by this
  contract; ID, kind, targets, and parameters remain integration data unless
  the game separately authorizes them;
- `summary`: rebuilt by Rin from the selected game-authored action
  description;
- `rationale`: a fixed stance-based UI sentence, never model reasoning or
  private actor text;
- `recalled_memory_ids`, `goal_id`, `boundary_id`, `policy_source`, and the
  full `proposed_goal`: private structured audit/integration data. Do not
  render these fields directly in a player UI; even the presence of
  `boundary_id` or a proposed goal can reveal hidden state;
- `status: pending`: Rin has not received the game's outcome; it is not an
  action waiting for Rin to activate it;

`boundary_id` is an optional additive v1 response field derived by the
runtime when request tags trigger an actor boundary. Tolerant SDK decoders
must preserve or ignore unknown additive fields. It is not supplied by the
model and is not a presentation string.

For events or Snapshots created before the presentation gate, State, Replay,
Snapshot export, and exact retry return the reconstructed player fields. The
raw append-only event or embedded Restore Snapshot is not rewritten. An exact
retry therefore preserves the original action, audit IDs, revision, and head,
but its legacy `summary`/`rationale` may be upgraded.

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

Clients must inspect `job.error.code` before treating a terminal `failed` Job
as proof that no Proposal exists. `proposal_outcome_unknown` means Rin could
not determine or confirm whether the Proposal event became durable. Although
the Job is terminal, this code is not a confirmed no-Proposal result: keep the
durable Proposal Attempt, re-POST its exact same `request_id` and payload, then
resume GET using the returned (normally unchanged) Job ID. Do not execute an
offline fallback or start another Session mutation until reconciliation. A
direct synchronous uncertainty may surface as HTTP `500`; other mutations
blocked behind that uncertainty return HTTP `409` with the same code.

Cancel with:

`DELETE /v1/jobs/{job_id}`

The response is the stable terminal job state. Canceling a running proposal
waits for its in-flight Engine mutation to settle; if the Proposal already won
the durable-write race, DELETE returns `succeeded` with that Proposal instead
of hiding it as canceled. Clients must consume this response.

While a Proposal Job record remains in the process, repeated submissions with
the same Session and `request_id` return that Job; a different payload returns
`request_id_conflict`. Job metadata is not durable and may disappear after its
retention TTL or a restart. The resulting Proposal is a durable Session
mutation: resubmitting its exact request lets the Engine return the original
Proposal even when the process-local Job record must be reconstructed. The
queue is bounded; when full it returns `429 jobs_queue_full`.

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

The same `request_id` and payload return the same Job only while that
process-local record remains retained. Semantically identical requests with
different IDs may hit the short-lived cache. Generation Jobs do not enter the
event log; after Job eviction or a sidecar restart, the same request may invoke
the provider again and produce another result. A game must validate the result
against its own content contract before accepting it into canon. Provider
failure never generates replacement story automatically; callers must supply
offline content.

## Commit

`POST /v1/action/commit`

Commit records the authoritative outcome after the game applies or rejects a
Proposal; it is not permission to execute. The game must revalidate and handle
the action on its owning thread before sending Commit. `accepted=true` means
the action actually took effect. `accepted=false` means that proposed effect
did not occur.

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
proposal does not modify actor memories, facts, or goals; send facts learned
from a failed attempt separately through `observe`. With
`outcome-reporting-v1`, rejected reports must omit `facts` and `goal_updates`,
and accepted reports may contain at most one update per Goal.

With `outcome-reporting-v1`, `tick` is when the action happened or was
rejected. It cannot predate the Proposal tick, but it may be older than the
current Session tick when the report arrives. Rin records an Outcome the game
already handled even if the current Revision or World Revision has advanced
since the Proposal. Resolved Proposal state includes `outcome_event_id` and
`outcome_tick`; Facts include `observed_tick`, and Goals include `updated_tick`
plus an unclamped `progress_accumulator`, a `status_explicit` marker, and
independent `status_updated_tick`/`status_source_event_id` ordering. These
server-owned values preserve occurrence-time ordering and order-independent
progress deltas when reports arrive late. Sessions without the Feature retain
the legacy stale/tick validation and arrival-order reducer. Retry a timeout or
temporary failure only with the same `request_id`; never execute the game
action again. See
[action outcome reporting](outcome-reporting.md) for the complete merge,
Outbox, late-outcome, and migration rules.

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
selected actions, the game may use `POST /v1/action/commit-batch` to record at
most one result per actor. Every item must come from the same original
`based_on_world_revision`, although Rin's current revision may have advanced
when the report arrives. Mixed original revisions or any invalid item reject
the entire batch without partial mutation.

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

## State closure and bounded retention

Every successful mutation produces a complete State that passes the same
structural validation used by Snapshot and Restore. Reducers validate an
isolated candidate before Store append, so an invalid transition is rejected
without a partial in-memory or durable update. Dynamic references such as Fact
visibility must name actors in the Session. With `belief-conflicts-v1`,
`beliefs` and `belief_sets` have exactly the same keys and selected Fact.

Retained collections use these protocol bounds:

| Collection | Bound | Full-capacity behavior |
|---|---:|---|
| Actor Goals | 32, including distinct pending ProposedGoal reservations | Reject a new reservation; never silently drop a Goal |
| Actor detailed Memories | 128 | Archive into a Summary when `memory-archive-v1` is enabled; otherwise evict details and remove their recalled references |
| Actor Memory Summaries | 32 | Deterministically merge older summaries; level saturates at 16 |
| Actor Beliefs / BeliefSets | 256 keys | Deterministically evict the oldest projected key and its paired set |
| Actor RecentActions | 32 | Retain the latest game-occurrence outcomes |
| Session Proposals | 64 | Evict only resolved proposals; fail closed when all retained proposals are pending |
| Session Arbitrations | 32 | Retain the latest records |
| Session Receipts | 1024 | Retain the newest revision generation as a hot projection; permanent request identity is stored separately |

Recall counts saturate at 1,000,000. Memory compaction rewrites a Proposal or
RecentAction reference to the replacement Summary ID; non-archive eviction
removes the unavailable ID. Revisions, ticks, selected belief sources, Goal
status sources, and visibility actors retained by Memory, Summary, Belief,
Activity, Goal, and outcome metadata must remain inside the containing State.
Retained Proposal and Arbitration tick fields are not upper-bounded by
`state.tick` and may describe work ahead of it; live Propose and Arbitrate
requests still reject tick regression. `nil` and an empty Fact visibility list
are the same JSON contract value.

`MemorySummary.summary` is a deterministic, lossy projection capped at 1,000
Unicode code points. Each merge reserves an oldest text anchor, weights
remaining text budget by importance with a recency tie-break, and reserves a
newest anchor. Importance improves the bounded retention opportunity; it does
not guarantee permanent verbatim survival. Each source-ID list is capped at
64, retains its oldest and newest known entries, and samples the intervening
tick range. It is representative evidence, not an exhaustive source ledger.
The direct oldest-four merge lineage and Summary-ID derivation remain stable
so `recalled_memory_ids` persisted in older Proposal events still replay.

Removing detail from Memory or Summary State is cognition compaction, not
deletion of authority or privacy erasure. `events.jsonl`, Identifier History,
backups, checkpoints, and retained Snapshots can still contain the underlying
text or identifiers. Use the deployment's separately governed deletion and
backup process for an actual erasure request.

Request and Event IDs remain reserved by Identifier History after every bounded
State projection has been evicted. This permanent ledger is reconstructed from
the event log or a validated checkpoint plus its event tail when that Session
is first recovered, and is carried by Snapshot and Replay independently from
`SessionState`. Applications should still generate globally unique IDs: that
avoids collisions when a legacy Snapshot cannot prove its complete pre-export
history.

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
  "expected_binding": {
    "game_id": "my-game",
    "content_id": "base-story",
    "content_version": "1.0.0",
    "content_hash": "sha256:..."
  },
  "snapshot": {
    "protocol_version": "rin.protocol/v1",
    "state_hash": "...",
    "state": {},
    "identifier_history": {
      "version": "identifier-history-v1",
      "coverage_complete": true,
      "requests": {},
      "events": {}
    },
    "identifier_history_hash": "..."
  }
}
```

`expected_binding` is mandatory and must come from the running game's trusted
content manifest, never from the Snapshot being imported. Restore verifies the
three participants in the operation: `expected_binding` must equal
`snapshot.state.binding`, and an existing target Session must carry that same
binding. On a fresh target the first two values establish the new Session's
binding; on an existing target all three must match. Any mismatch is
`409 binding_mismatch`.

Restore also rejects a different Session ID or an invalid checksum. Rin
validates a cloned State before computing or saving a Snapshot, so every
successfully returned Snapshot immediately passes `ValidateSnapshot` and can
be imported into a fresh or non-exhausted matching Session.

`state_hash` covers the bounded `state`; `identifier_history_hash` separately
covers the canonical JSON form of `identifier_history`; both are SHA-256
checksums. They detect accidental corruption and changes made without updating
the checksum, but they are not signatures, do not authenticate provenance, and
do not protect against a party that can edit the Snapshot and recompute them.
A Snapshot is trusted, opaque serialized state: do not accept it from an
untrusted source or edit it, and protect its file and body at the same level as
the event log. The trusted runtime manifest remains the source of
`expected_binding`.

The history uses version `identifier-history-v1`. Its request entries retain
canonical request digests and original result coordinates or typed
Proposal/Arbitration results; its event entries retain every accepted Event
ID. `coverage_complete=true` means the producer knows the complete imported
lineage. Supplying only one of the two history fields, malformed history, or a
checksum mismatch is `400 invalid_snapshot`.

The compact canonical JSON encoding of the complete Snapshot must not exceed
16 MiB. Snapshot creation and Replay fail atomically with
`413 snapshot_too_large` when it does. Restore returns that error when the
complete request still fits the configured request-body limit; a request that
exceeds that outer limit (32 MiB by default) is rejected during decoding first
as `413 body_too_large`. No State or Identifier History is truncated. All
bundled clients' default 32 MiB response limit deliberately leaves room around
the Snapshot for envelopes and durable records.

Upgrade compatibility is intentionally asymmetric. A new HTTP Restore request
without `expected_binding` is `400 invalid_request`; callers must upgrade and
source the field from their trusted manifest. Existing on-disk
`session.restored` events remain replayable, including events whose embedded
Snapshot is now above 16 MiB. When rebuilding request identity, Rin
reconstructs the legacy four-field Restore request shape and preserves its
digest semantics. When a legacy event's Snapshot still fits the inline limit,
a new-schema exact retry with the trusted matching `expected_binding`
recognizes the old digest and returns the original result as a duplicate. An
oversized legacy event can still be opened and replayed from disk, but
cannot be retransmitted through the inline API; no streaming Snapshot
transport is currently provided.

A legacy v1 Snapshot without these two fields remains restorable, but Rin
imports it with `coverage_complete=false`. It can seed only IDs still
discoverable from that Snapshot; IDs evicted before export are unknowable.
Incomplete coverage is sticky across every later Snapshot and Restore merge
and is never promoted to complete. Every ID first accepted after import is
still retained permanently. Applications using legacy saves must therefore
continue to generate globally unique IDs.

Restore writes a new local event-chain generation. Retained nested revision
metadata is rebased to the Restore event; a retained Proposal references the
preceding local revision and head hash. On a fresh import that base is revision
zero with an empty head hash. Imported historical Receipt revisions become
zero before the new Restore Receipt is inserted, so a full 1,024-entry map
cannot evict the operation that just succeeded. World revision advances
without wrapping; importing an already-maximal world revision keeps it
saturated, while later world mutations fail closed.

Restore unions the target Session's current Identifier History with the
Snapshot history before adding the Restore request. IDs from the current branch
remain tombstoned even when Restore abandons that branch. A verified mapping
that disagrees between the histories rejects Restore atomically with
`409 identifier_history_conflict`; neither history is overwritten. Legacy log
entries which reused an ID, or whose exact typed request digest cannot be
recovered, remain readable as ambiguous tombstones. Any later attempt to use
such a request ID fails closed with `409 request_id_conflict`.

An original duplicate result imported from another Restore generation may
contain the revision/head of its source generation and need not identify an
event replayable in the new local chain. It remains the immutable receipt for
that operation; query State for the current local head.

With `outcome-reporting-v1`, Restore retains pending proposals
for two durable recovery states: an unresolved Proposal Attempt received before
the game handled it, or an already-handled operation whose saved Outcome Outbox
still needs to report. A restored Proposal never authorizes execution. The game
must use its saved Attempt and applied-operation marker to distinguish those
states, revalidate an unhandled action before handling it, and never repeat an
already-handled action. Sessions without the Feature retain legacy behavior
and clear restored proposals.

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

Use `next_after_revision` for the next page. `limit` is 1 to 256. Each request
captures its own `current_revision`; mutations may advance the Session between
pages. A client that needs one fixed audit window should keep the first
response's `current_revision` as its upper bound and stop there. With the
bundled file store, a healthy revision index makes each steady-state page a
bounded range read rather than a complete event-log replay. A missing or
invalid index incurs one full-log rebuild; a custom Store without range
support may still use a full `Load` fallback.

`POST /v1/session/replay` validates the newest usable internal checkpoint at
or before the selected revision, then runs the normal reducer and hash-chain
verification over its remaining event tail. With no usable checkpoint it
falls back to genesis. It then returns an in-memory Snapshot:

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1","revision":42}
```

Replay includes actor memories and story state present at that revision, so
it keeps the Session API authentication boundary and is not a redacted log
endpoint. The replayed `state` is revision-specific, but its Identifier History
contains the complete local-lineage tombstone set, including IDs first used
after the selected State revision. This deliberate asymmetry prevents
restoring an old Replay Snapshot from making a later ID reusable. Identifier
result revisions may therefore be greater than `snapshot.state.revision`.

After a Session's first lazy load, Timeline and Replay capture the live head
and required Identifier History under the mutation lock, then release that
lock before range I/O and reducer work. The first operation on an unloaded
Session must first complete its serialized recovery. Checkpoints only
accelerate reconstruction: they are versioned, checksummed derived caches
anchored to an event, not protocol Snapshots or event-log authority. An
operator can use `Engine.VerifyAll()` to ignore checkpoints and audit every
Session from genesis to head. After a successful lazy recovery, Runtime
best-effort asynchronously queues a checkpoint at the recovered head when no
usable checkpoint was selected, or when
`head revision / selected checkpoint revision >= 2`. The checkpoint may not be
durable when the successful Session read returns; a checkpoint build or write
failure does not turn that read into an error.

Identifier History and its retained Proposal/Arbitration results grow linearly
with successful mutations. It can contain historical model-authored text which
bounded cognition State has already evicted, so protect Snapshot files and
bodies at the same sensitivity level as the event log. Once the complete
compact Snapshot exceeds 16 MiB it cannot be returned or restored inline.
No streaming Snapshot transport is currently provided; Identifier History is
never silently truncated.

## Common errors

| HTTP | Code | Meaning |
| --- | --- | --- |
| `400` | `invalid_json` / `invalid_request` | JSON or field-contract error |
| `400` | `invalid_snapshot` | State or Identifier History is malformed or its hash does not match |
| `401` | `unauthorized` | Missing or incorrect Bearer token |
| `404` | `session_not_found` / `unknown_actor` | Entity does not exist |
| `404` | `revision_not_found` | Replay revision does not exist |
| `500` | `store_load_failed` | Durable Session storage could not be read; never treat this as `session_not_found` |
| `500` | `replay_failed` | Durable Session recovery or replay validation failed; never create a replacement Session |
| `409` | `request_id_conflict` | Request ID is ambiguous or was already bound to another kind or payload |
| `409` | `event_exists` | Event ID is already reserved in this Session lineage |
| `409` | `binding_mismatch` | Trusted `expected_binding`, imported Snapshot, or existing Session binding differs |
| `409` | `identifier_history_conflict` | Restore histories contain incompatible verified identities |
| `409` / `500` | `mutation_outcome_unknown` | A non-Proposal mutation may be durable; retain it and retry only the exact request before any other mutation |
| `409` | `state_changed` / `proposal_stale` | Base state changed during Proposal generation or pre-application Arbitration |
| `409` | `proposal_base_mismatch` | A Batch Outcome mixes original world revisions |
| `409` | `actor_not_due` | Actor has not reached its thinking tick |
| `200` Job / `409` / `500` | `proposal_outcome_unknown` | Proposal durability is unresolved; retain the exact attempt and reconcile it without fallback |
| `422` | `no_safe_action` | Boundary triggered without a safe candidate |
| `413` | `body_too_large` | Request exceeds the configured request-body limit before Snapshot validation |
| `413` | `snapshot_too_large` | Decoded complete compact Snapshot exceeds the 16 MiB inline limit; nothing is truncated |
| `429` | `jobs_queue_full` / `jobs_capacity` | Proposal queue or retention is full |
| `429` | `generation_queue_full` / `generation_capacity` | Generation queue or retention is full |
| `503` | `jobs_unavailable` / `jobs_closed` | Proposal jobs are disabled or closing |
| `503` | `generation_unavailable` / `generation_closed` | Generation is disabled or closing |

The service never places event payloads, tokens, internal paths, or raw model
responses in error messages.

# Action outcome reporting

[English](outcome-reporting.md) | [简体中文](outcome-reporting.zh-CN.md)

This document defines the Proposal, game application, and Commit transaction
semantics for `rin.protocol/v1`. New sessions must include
`outcome-reporting-v1` in `CreateSessionRequest.features` to opt in. Sessions
without that feature keep the historical commit-as-fresh-head checks and
clamped, arrival-ordered reducer behavior so existing event logs replay
unchanged.

Rin `0.6.0` is Preview. Required fields and wire shapes are authoritative in
[`api/openapi.json`](../api/openapi.json).

## One world authority

The game engine is the sole authority for world facts. A Rin Proposal is a
pre-application suggestion. Commit records the result after the game has
handled that Proposal:

```text
Rin produces a Proposal
→ the game revalidates the action, target, and preconditions on its owning thread
→ the game applies the action or rejects it
→ the game adds the result to its durable Outcome Outbox
→ the game reports the accepted/rejected result to Rin with Commit
```

Rin does not execute a game action through Commit, and Commit success must not
cause the game to execute the action again.

For v1 wire compatibility, `/v1/action/commit`, `CommitRequest`, `accepted`,
and the adapter-local `committable` field retain their existing names. They
describe outcome-recording capability, not authorization or execution by Rin.

## Field semantics

- `accepted=true` means the game confirms that the proposed action actually
  took effect and became canon.
- `accepted=false` means the game confirms that the proposed world effect did
  not occur. `outcome` may contain a bounded audit reason; observations learned
  from the failure should be sent separately through `observe`. A rejected
  report must not carry `facts` or `goal_updates`.
- `status=pending` means Rin has not yet received and settled the game result.
  It does not mean the action is waiting for Rin to activate it.
- `committable=true` means the Proposal ID can be reported to the current
  sidecar. It is not execution authorization and does not replace the game's
  local freshness check before application.
- `tick` is the game tick when the action happened or was rejected. It cannot
  predate the Proposal tick, but it may be older than the current Session tick
  when the report arrives.

`accepted` is required in a Commit and in every Batch Commit item. A rejection
must serialize explicit `false`; omission or `null` is not a rejection and
returns `400 invalid_request`. Every integer in the public JSON request must
also be within the exact interoperable range `-9007199254740991` through
`9007199254740991`, with the field's narrower rules applied afterward.

Before application, the game must re-read Session state and check its own
authoritative preconditions. With `arbitration-v1`, require
`state.world_revision == proposal.based_on_world_revision` (or arbitrate the
proposal set). Without arbitration, require the retained Proposal to remain
`pending` and `state.revision == proposal.created_revision`. The
`based_on_revision` and `based_on_head_hash` fields identify the state before
the Proposal event and are audit context; they are not compared directly with
the post-Proposal Session head. If freshness or game preconditions fail, the
game must not apply the action and may report `accepted=false`.

A Proposal Job timeout is not proof that no Proposal exists. Retry submission
or lookup with the same request ID/job ID, and consume the final DELETE
response: cancellation may lose a race to a Proposal that was already
persisted. While delivery or cancellation is unconfirmed, fail closed and do
not execute an offline fallback. A fallback is safe only when the integration
knows no online Proposal was created (for example, the Sidecar was disabled or
the initial connection was definitively refused).

## Late outcomes

After the game applies an action, observations, other actor outcomes, or network
delay may already have advanced Rin's state. That report is a late
authoritative fact, not an error. Commit does not reject it merely because the
current Revision, World Revision, or Session tick has advanced.

Rin merges accepted late outcomes by their game occurrence tick:

- scheduling never moves backward;
- accepted actions and episodic memories remain ordered by occurrence time;
- a Fact is stamped with server-owned `observed_tick`, so an older report
  cannot overwrite a newer value for the same subject and predicate;
- a Goal is stamped with `updated_tick`; its server-owned
  `progress_accumulator` retains the unclamped sum so positive and negative
  deltas remain commutative; `status_explicit` distinguishes a game-supplied
  status from automatic active/completed projection, while
  `status_updated_tick` and `status_source_event_id` order explicit statuses
  independently from progress-only updates (event ID breaks a same-tick tie);
- resolved Proposals carry `outcome_event_id` and `outcome_tick`, including
  rejected outcomes, so their event IDs remain auditable while retained.

These fields are response/state metadata and request DTOs must not set them
(leave them omitted or at their zero value). Callers supply occurrence time
through the enclosing Observe or Commit `tick`; Rin derives the metadata from
that authoritative request field.

`state_changed` while producing a Proposal and `proposal_stale` during
pre-application Arbitration still reject obsolete suggestions. They are not
used to reject an outcome that the game has already handled.

## Batch outcomes

`/v1/action/commit-batch` requires `arbitration-v1` and atomically records a set
of outcomes. The apply-then-report and late-outcome rules in this document also
require `outcome-reporting-v1`. Every item must come from the same original
`based_on_world_revision`, but that revision may be older than Rin's current
revision when the report arrives. Every item also shares the enclosing
`BatchCommitRequest.tick` as its actual occurrence tick; group outcomes by tick
or use individual Commit calls when their occurrence times differ. Mixing
original world revisions returns `proposal_base_mismatch` without partial
mutation.

## Outbox and retries

Before an asynchronous Proposal submit, persist a separate Proposal Attempt
containing the complete request, game operation identity, and optional Job ID.
`proposal_outcome_unknown` keeps that attempt and blocks new turns. Resume its
exact request/job identity until Rin returns a Proposal or confirms a terminal
no-Proposal state; a terminal Job carrying this code is still unresolved.
When a Proposal succeeds, remove the attempt only in the same authoritative
transaction described below; this closes the crash window between receiving a
Proposal and persisting its eventual report.

The game should apply an action and persist an Outcome Outbox entry in the same
authoritative transaction. An entry contains at least:

- stable Commit `request_id` and `event_id` values which are unique throughout
  the Session lineage;
- `proposal_id`, occurrence tick, accepted, and outcome;
- any tags, facts, and goal updates needed by the report.

An accepted report contains at most one update for each Goal. This removes
array-order ambiguity when occurrence-time updates merge.

On a timeout or temporary error, the game reports the exact same typed payload
again with the same `request_id`; it must never apply the action again. Rin
binds that ID to the canonical complete request digest. Changing an Event ID,
tick, accepted value, outcome, tag, Fact, Goal update, or any other typed field
returns `request_id_conflict` instead of creating a second interpretation.

`mutation_outcome_unknown` means Commit may already be durable but Rin could
not confirm it. Keep the Outbox entry, do not execute the action again, and
retry only that exact Commit. Other Session mutations are intentionally
blocked until it is reconciled. Proposal production uses the compatible
`proposal_outcome_unknown` code and requires the same exact-attempt recovery.

An exact duplicate success carries the original Commit revision/head with
`duplicate=true`; those fields are an immutable operation receipt, not Rin's
current State head. Remove an Outbox entry only after a normal success or this
explicit duplicate response. `event_exists` from a different request is a
conflict, not proof that this Outbox entry was recorded, and must not by itself
acknowledge or delete the entry.

Drain the Outbox before creating a game save, or save all unacknowledged
entries together with the matching Rin Snapshot and Proposal Attempts.
Snapshot Identifier History permanently carries accepted request and Event IDs
across restart and Restore. Restore retains pending Proposals both so an
unhandled saved Attempt can resume and be revalidated, and so an
already-handled operation's saved Outbox can still report its complete Facts,
Goal updates, recent action, and scheduling effects. A restored Proposal does
not authorize execution: the persisted Attempt and applied-operation marker
distinguish an unhandled action from one that must never run again.

When restoring that save, send mandatory `expected_binding` from the running
game's trusted content manifest—not from the Snapshot. It must match the
Snapshot binding and any existing target Session binding. Snapshot SHA-256
canonical checksums detect accidental corruption but do not authenticate
provenance or stop a party that can recompute them, so the Snapshot remains
trusted opaque state protected like the event log. Complete inline Snapshot
compact JSON is capped at 16 MiB, while server request bodies and bundled
client responses default to 32 MiB. `413 snapshot_too_large` never truncates
the saved lineage. No streaming Snapshot transport is currently provided.

If the sidecar session cannot be restored and therefore truly has no matching
Proposal, `observe` is a degraded reconciliation path for the authoritative
event's memory and Facts at its original occurrence tick. It cannot recreate
proposal-specific Goal deltas, recent-action history, or scheduling; represent
the resulting absolute world state as Facts and do not claim a complete Commit
reconciliation. Never repeat the action to obtain a new Proposal. An
`offline.*` Proposal can never be committed; report the actual fallback event
through `observe` after the sidecar recovers.

## Compatibility migration

An integration that currently commits first and applies only after success
must create a new session with `outcome-reporting-v1`, then migrate to game-side
validate and apply or reject first, followed by Commit. Request fields and HTTP
paths remain wire-compatible. The feature deliberately changes reducer
semantics and stored state metadata; it is never added automatically to an
existing session. Sessions and event logs without it keep their historical
replay result. Feature-enabled Proposal, Fact, and Goal state may include the
optional occurrence metadata described above; older pre-feature snapshots
remain readable under legacy semantics.

Identifier History is wire-additive and independent of
`outcome-reporting-v1`. A legacy Snapshot without it remains readable but is
marked with permanently incomplete coverage because IDs evicted before export
cannot be recovered. A game importing such a save must continue to use globally
unique request and Event IDs. History from the current branch and imported
Snapshot is unioned on Restore, so rolling back never authorizes reuse of an ID
from the abandoned future.

# Host action lifecycle

[English] | [简体中文](action-lifecycle.zh-CN.md)

This document defines the execution and recovery contract for Rin `0.7.0`
**Preview** and `rin.protocol/v2`.

## One owner for each responsibility

| Responsibility | Owner |
| --- | --- |
| Observe authoritative world state | game Host |
| Define capabilities and fully bound offers | game Host |
| Select one offered action | Rin policy |
| Accept, reject, start, cancel, and execute | game Host |
| Persist operation markers and the report outbox | game Host |
| Record memories, facts, goals, and audit history | Rin |

The model never receives a general command executor. An `ActionOffer` already
contains the capability version, descriptor digest, arguments, targets, epoch,
observation sequence, and deadline. Selecting an offer does not authorize any
other input.

## Lifecycle objects

`ActionInvocation` binds the selected offer to a stable `operation_id`.
`ActionRun` reports monotonic progress. `ActionOutcome` records a terminal
effect observed by the Host.

```text
offered → rejected
       └→ accepted → queued → running → succeeded
                              ├────────→ failed
                              ├────────→ cancelled
                              ├────────→ interrupted
                              ├────────→ stale
                              └────────→ outcome-unknown
```

A terminal Run requires an Outcome with the same operation ID, terminal
status, and expected epoch. Non-terminal Runs must not include an Outcome.
Facts and goal updates are allowed only with a terminal accepted report.

## Durable Pending Turn

Before the first proposal request, persist:

- stable operation ID;
- exact `ProposeRequest`, including Decision Window and Offers;
- any observation that must precede the proposal;
- Job ID after submission, before the first poll.

After restart, retry the exact request. If a process-local Job disappeared,
resubmit that same request. A new request ID would create a second logical
decision and is not recovery.

## Applying game effects

Choose and document one `HostDurability` profile:

- `advisory`: the adapter cannot prove crash-safe world mutation;
- `idempotent-action`: `Execute(operation_id)` and the game save prevent a
  second effect;
- `transactional-action`: game effect, operation marker, and exact report
  outbox entry commit atomically.

For an idempotent Host:

1. validate proposal identity, selected offer, epoch, observation sequence,
   capability digest, target validity, and deadline;
2. call the game-owned executor with a stable operation ID;
3. persist its marker and exact report;
4. retry only by operation ID.

For a transactional Host, steps 2 and 3 are one game transaction. Never claim
transactional durability because a JSON file was flushed near a game API call.

## Outcome Outbox

Rin reports are idempotent, but the Host must keep the exact request until it
receives a successful response. Drain the outbox before beginning another turn
for the same authority scope.

Do not convert a terminal report into an observation when an endpoint fails.
That loses proposal, invocation, and operation identity. Network ambiguity
means “retry this report,” not “invent a different event.”

An acknowledged report must never cause the game effect to run again. Event
replay reconstructs Rin state only.

## Rejection and failures

A rejection contains only proposal ID, event ID, decision, summary, and
optional tags. It has no Invocation, Run, Outcome, Fact, or Goal Update.

An accepted operation that failed to produce its intended effect is not a
rejection: report an accepted Invocation with terminal `failed`,
`interrupted`, `stale`, or `outcome-unknown` status and matching Outcome.
`outcome-unknown` is a durable state that requires later reconciliation; it is
not permission to run another action.

## Concurrent decisions

For `simultaneous` Decision Windows:

1. collect proposals based on the same authoritative window;
2. arbitrate shared targets deterministically;
3. apply accepted operations under the Host's concurrency rules;
4. use `BatchActionReportRequest` to atomically report the resulting decisions.

Every report in a batch retains its own operation lifecycle. The outer tick is
the authoritative occurrence tick shared by that batch.

## Review checklist

- Request, Event, Proposal, Offer, Window, and Operation IDs have distinct,
  stable meanings.
- Render frames and wall-clock time are not substituted for host step/event
  clocks.
- A saved Pending Turn is complete enough to resend byte-equivalent semantics.
- The selected offer is compared with the durable host-authored offer.
- Epoch and deadline are checked immediately before Execute.
- The executor does not accept model-generated method names or arguments.
- Applied markers and outbox entries have explicit size bounds.
- Restart, lost response, corrupt state, full outbox, and disk-write failures
  are tested.

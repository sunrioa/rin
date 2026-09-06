# Planning during execution

[English](lookahead.md) | [简体中文](lookahead.zh-CN.md)

The Internal Agent can prepare one conditional successor while an ordinary
Operation is accepted or running. The built-in `StructuredDecisionProvider`
supports this by default. The current Host action continues independently;
only the normal execution loop can adopt and submit the prepared action.

```mermaid
sequenceDiagram
    participant R as Agent Runtime
    participant H as Host / Control Plane
    participant M as Model
    R->>H: Submit action A
    H-->>R: Accepted / running
    par Execute
        H->>H: Execute A
    and Prepare one successor
        R->>M: Current observation + conditional next-step request
        M-->>R: NextStepDraft B, or none
    end
    H-->>R: Confirmed successful outcome A
    R->>H: Wait for required Plan projection; observe current state
    R->>R: Revalidate draft against actual result and current state
    alt Ready and applicable
        R->>H: Submit B through fresh Host binding and Policy
    else Missing, late, expired, or invalid
        R->>M: Normal decision from actual state
        M-->>R: Action / wait / completion request
    end
```

For example, while an Actor approaches a tree, the model can prepare a harvest
action against the already observed tree. Arrival, required facts, the target,
and the capability must still be valid before submission. A successor that
depends on an unknown output or a newly created, unseen target is left to normal
deliberation after the outcome arrives.

## What a draft can do

`LookaheadProvider` is an optional extension to `ModelProvider`. It supplies a
token reservation without network I/O and makes one conditional model request.
`NextStepDraft` can contain one ordinary capability, arguments, observed target
handles, an existing Plan step, and up to eight scalar Host-fact preconditions.
The expected value may describe a future state, but its fact ID must already
have been published. A `none` draft declines to predict a successor.

The model contract cannot complete a task, revise a Plan, write memories, claim
an outcome, or construct a bound action. Full schemas for up to four ordinary
capabilities accompany the bounded context; lookahead has no inspection round.
Macro activation uses the existing path. An ordinary child action inside a
running macro can prepare its next ordinary sibling.

After A succeeds, the normal loop waits for its required `task-plan` projection
acknowledgement, collects a fresh observation, and checks:

- the same task, goal, controller lease, Epoch, attention state, and Plan intent;
- the immediate preceding Operation's confirmed successful Host outcome;
- an observation at least as recent as both that outcome and the preview;
- the actual active Plan step, unchanged capability digest, and expected facts
  with the same Host subjects;
- the original Host target references still being present, and valid arguments.

Observation sequence, Plan revision, and Plan phase can advance normally after
A. Rewriting Plan intent invalidates the draft even if a step ID is reused.
Target handles are resolved again from the fresh observation. Submission still
performs Host binding, Policy, confirmation where required, and the ordinary
durable action-intent protocol. Completion keeps the caller's acceptance policy.

## Configuration and cost

Add this optional section to the existing Agent JSON configuration:

```json
{
  "runtime": {
    "lookahead": {
      "disabled": false,
      "max_concurrent": 2,
      "timeout_millis": 10000,
      "draft_ttl_millis": 60000
    }
  }
}
```

These are the defaults. `max_concurrent` accepts 1–32 simultaneous background
jobs per runtime. Timeout accepts 100–60000 ms; TTL starts when the job is created,
must be at least the timeout, and cannot exceed 300000 ms. The runtime retains
at most 256 jobs, including completed candidates awaiting an execution boundary.
An occupied pool skips work without queuing another background request.

The Console's Agent settings include “执行时提前准备下一步”. Saving configuration
requires the existing daemon restart to take effect. The Management API exposes
the advanced limits and preserves them when the checkbox is changed. Providers
that do not implement `LookaheadProvider` keep the normal serial path. Custom
providers must support concurrent calls and honor context cancellation.

Each Operation gets at most one started lookahead attempt. Before contacting
the model, the task durably increments `model_calls` and reserves tokens. It
starts only when two model calls and twice the reservation remain, leaving
allowance for normal fallback. The built-in reservation uses prepared prompt
UTF-8 bytes, schema overhead, and the output allowance, capped at 2048 output
tokens. This is a conservative runtime estimate, not a provider billing limit.

Reported usage replaces the reservation, including invalid responses that carry
known usage. Missing or invalid usage retains the reserved charge. The larger of
reported total tokens and prompt-plus-completion tokens is charged. A reported
overrun remains recordable while the existing Operation settles; it prevents
further model decisions. Transport retries follow the provider's existing
resilience settings; `model_calls` counts logical calls, not HTTP attempts.

## Cancellation, recovery, and visibility

Context assembly and inference run outside the task execution lock and scheduler
worker. Cancellation, pause, changed task authority, or a new attention signal
invalidates the candidate without waiting for its provider to exit. Timeout or
preview failure does not pause the current action. At the next decision boundary,
an unfinished draft is cancelled and normal deliberation starts immediately.
Late results can settle usage but cannot replace a decision already made.

Drafts are process-local. A restart discards them, conservatively settles any
durable reservation once, and continues tracking the original Operation without
resubmitting it. Task projections use `rin.cognition.tasks/v6`; v3/v4/v5 imports
remain supported. Embedders must call `AgentRuntime.Close()` before closing its
Task, Memory, and Plan stores; the daemon handles this lifecycle automatically.

Task APIs and the Console show `preparing`, `running`, `ready`, `adopted`, or
`discarded`, with call/adoption/discard counts and outstanding token reservations.
The timeline records `lookahead.*` events, discard codes, measured provider
latency, and available usage. A ready candidate is still waiting for verified
execution conditions. Adoption counts show reuse, not proof of goal completion.

Concurrency regression tests hold A open while B is prepared, then exercise
fresh binding, Plan acknowledgement and rewrites, changed facts/targets/Epoch,
timeouts, cancellation, pool saturation, and SQLite restart recovery. They prove
execution and preparation overlap. Actual time saved depends on Host duration,
provider latency, and adoption rate; no production speedup is assumed.

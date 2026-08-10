# Bounded Task Plan DSL

Rin's Go `planner` package is an engine-neutral deterministic state machine for Host-authored
plans. It does not read world state, resolve coordinates, call a model, or execute capabilities.
A Host turns authoritative facts into complete Offers and verifies effects on the game thread.
Rin does not centrally execute a game adapter's plan.

## Shape and validation

`Plan` contains `action`, `branch`, and bounded `loop` nodes. `Validate` checks identifiers,
the `low|moderate|high|critical` risk enum, node/depth/priority/attempt limits, exclusive control
ownership, references, graph cycles, and budgets. An action owns a registered capability. A
branch owns distinct `then` and `else` roots. A loop owns children and an explicit
`max_iterations`; implicit graph cycles are rejected.

The wire contract is versioned independently from plan revisions. Every document must carry
`"schema_version": 1`; unsupported versions are rejected before graph validation. The canonical
JSON Schema at `planner/schema/plan-v1.schema.json` and fixture at
`planner/testdata/plan-v1.json` are tested against the Go runtime. `revision` identifies
changes to one authored plan, while `schema_version` identifies the DSL shape and semantics.

## Deterministic transitions

The Host uses the state machine in this order:

```go
state, err = plan.Advance(state, facts, absoluteTick)
actions := plan.Ready(state, nil)
// Execute one complete Host-bound action and verify its postcondition.
state, err = plan.Apply(state, actions[0].ID, absoluteTick, verifiedMutations)
// Or record a verified failure without completing the node.
state, err = plan.Fail(state, actions[0].ID, absoluteTick, verifiedMutations)
```

`Advance` records exactly one branch path, skips the other path, activates loop children, resets
them between iterations, and closes a loop at its fact condition or iteration bound. `Ready`
returns action nodes only and orders them by priority and then ID. `Apply` and `Fail` consume the
attempt and global step budgets. Exhausting an action's retries records an explicit `Failed` state
and stops further scheduling. `Done` reports either a successful or failed terminal plan;
`Succeeded` distinguishes the successful result.

Absolute ticks are measured from the first transition, so a plan started in an old world timeline
does not fail merely because the engine clock is already large. They may never move backwards. All
transitions copy State maps. `ValidateState` rejects inconsistent or forged state, including a
`Skipped` node without an unselected branch ancestor and an action completed without an attempt.

`State.steps` is the cumulative count of verified action attempts. A loop reset removes the prior
iteration's per-node `attempts`, so the state machine moves those removed attempts into
`retired_steps`. Persisted state must always satisfy
`steps = sum(attempts) + retired_steps`; `ValidateState` also bounds `retired_steps` by the recorded
loop iterations and each controlled subtree's maximum attempts. This field is Host-maintained
execution history, not an Agent-editable budget override.

Adapters persist State together with the plan revision, Offer digest, Epoch, and Outcome. An
uncertain world effect must become paused or `outcome_unknown`; it must not call `Apply` or `Fail`
and must not be replayed automatically. Non-Go adapters can implement the same transition contract,
but Minecraft block, recipe, pathfinding, and permission rules remain adapter-owned.

Rin's Minecraft adapter does not currently consume this executable Plan v1 document. Its
`active_plan.task_graph` is a versioned, read-only projection of a Host-owned Java controller so
external agents can observe verified progress. It must not be submitted back as a Planner plan or
treated as permission to execute arbitrary graph nodes.

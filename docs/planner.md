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

Adapters persist State together with the plan revision, Offer digest, Epoch, and Outcome. An
uncertain world effect must become paused or `outcome_unknown`; it must not call `Apply` or `Fail`
and must not be replayed automatically. Non-Go adapters can implement the same transition contract,
but Minecraft block, recipe, pathfinding, and permission rules remain adapter-owned.

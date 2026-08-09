# Bounded Task Plan DSL

Rin's `planner` package defines an engine-neutral plan shape and budget gate. It does not read
world state, resolve coordinates, or execute capabilities. A Host turns authoritative facts into
complete Offers and verifies their effects on the game thread.

`Plan` contains `action`, `branch`, and bounded `loop` nodes. `Validate` checks identifiers,
node/depth/priority/attempt limits, references, graph cycles, and budgets. `Ready` orders
candidates by priority and then ID. `Plan.Allows` checks step, world-mutation, tick, dependency,
and per-node attempt limits. `Plan.Apply` returns a new State
after a verified result without mutating the caller's maps.

Adapters persist the cursor, revision, Offer digest, Epoch, and Outcome. An uncertain world effect
must become paused or `outcome_unknown`; it must not be replayed automatically. The Minecraft
adapter is implemented in `CompanionDynamicTaskController`, while block, recipe, pathfinding, and
permission rules remain adapter-owned.

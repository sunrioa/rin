# Session semantic baseline

[简体中文](semantic-baseline.zh-CN.md) | [English](semantic-baseline.md)

Rin Protocol v1 has one safe baseline for every newly created Session:
`outcome-reporting-v1`. A new Create request that omits it fails with
`400 invalid_request` on `features`. This removes the previous silent choice
between game-authoritative apply-then-report transactions and legacy
fresh-head Commit behavior.

`GET /health` returns both:

- `features`: every capability this Runtime understands;
- `recommended_features`: the mandatory baseline for a new Session.

The JavaScript/TypeScript and C# SDKs expose this as `safeBaseline`; the former
`authoritative` name remains an alias for source compatibility. Capability
negotiation defaults to the safe baseline.

## Compatibility boundary

The Event Log remains authoritative. A pre-baseline v1 history without
`outcome-reporting-v1` still replays with its original reducer behavior, and an
exact retry of its original Create request still returns the durable result.
Session Transfer may move that same legacy lineage without changing it.

The baseline is enforced only when establishing a new lineage:

- a fresh Create must include `outcome-reporting-v1`;
- a Restore into an absent Session ID must contain a Snapshot that already has
  `outcome-reporting-v1`;
- Restore/replay/exact retry of an existing legacy lineage remains available;
- `EngineOptions.AllowLegacySessionCreation` is an explicit, embedded-runtime
  migration escape hatch. The bundled Sidecar never enables it.

Do not use that escape hatch for a new integration. To migrate semantics,
create a distinct baseline Session and move authoritative game state using the
game-side transaction described in the migration guide; never edit an Event
Log or Snapshot Feature list.

## Optional capability matrix

Correct transaction authority is no longer optional. The remaining switches
are capabilities whose endpoints/data are meaningful only when the game
implements them:

| Optional Feature | Independent of | Requires game-side behavior |
| --- | --- | --- |
| `memory-archive-v1` | all other optional Features | Accept deterministic lossy summaries and retain privacy policy |
| `belief-conflicts-v1` | memory archive, goals, activity, arbitration | Preserve sourced contradictory claims |
| `goal-candidates-v1` | memory/beliefs/activity; composes with arbitration | Supply complete candidate Goals and adopt only after accepted outcome |
| `actor-activity-v1` | memory/beliefs/goals/arbitration | Persist region plus awake/dormant transitions |
| `arbitration-v1` | memory/beliefs/activity; composes with candidate Goals | Track world revision and apply one atomic Batch Outcome |

All 32 combinations of these five optional Features are structurally covered
with the mandatory baseline by
`TestSafeBaselineSupportsEveryOptionalFeatureCombination`. Feature-specific
tests then cover enabled and disabled endpoint behavior; state-invariant tests
cover interactions between outcome reporting, arbitration, candidate Goals,
activity, and conflicting Beliefs. Legacy tests use the explicit migration
option and verify replay/exact retry separately.

The `full` SDK preset enables every optional capability for conformance and
advanced integrations; it is not a recommendation. Start with `safeBaseline`
and add a capability only when the game needs and persists its contract.

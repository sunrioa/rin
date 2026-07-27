# Player-value evidence and release gates

[English](player-value.md) | [简体中文](player-value.zh-CN.md)

## What was tested

The supported runtime is the installable Node.js 18+ terminal story
[`Last Station`](../examples/terminal-story/README.md), using the priority
JavaScript SDK on the real File Store Sidecar. A player tells Mira a drink
preference; after a time skip, bounded memory recall influences a game-authored
allowlisted action. The comparison is a persistent deterministic rule tree,
not a deliberately stateless baseline.

The checked-in 100-turn measurement was captured on darwin/arm64 with Node
22.23.1 and the deterministic policy. It is reproducible, not a universal
performance promise. The raw evidence is
[`benchmark-darwin-arm64.json`](../examples/terminal-story/evidence/benchmark-darwin-arm64.json).

| Measure | Rin safe turn | Persistent rule tree |
| --- | ---: | ---: |
| Integration nonblank lines | 158 | 19 |
| P50 | 68.87 ms | 10.79 ms |
| P95 | 82.32 ms | 12.98 ms |
| Player-visible choice | tea | tea |
| Provider calls / cost | 0 / USD 0 | 0 / USD 0 |

The benchmark reloads the game save between every turn. Sidecar readiness took
73.28 ms. Rin retained 536,612 bytes after 100 turns;
the deliberately conservative linear projection at 60 turns/hour is
32,196,720 bytes for 100 hours. Startup-only local mode completed in 125.83 ms
and preserved the authored tea result. A failure after Rin mutation begins
fails closed because absence of a response is not proof of absence.

## Honest conclusion

Rin now produces observable offline memory behavior: the recalled
`preference.tea` tag selects `offer.tea` without leaking private memory text.
It also supplies generalized history, audit, bounded recall, exact retry, and
crash-safe outcome reconciliation.

It does **not** beat a purpose-built persistent rule tree for one preference.
The measured slice requires 139 more integration lines and roughly 6.3x P95
latency for identical visible output. Rin is therefore unjustified for this
small rule. The value hypothesis is only plausible when a game has enough
independent memories, actors, and authored actions that bespoke persistence
and branching stop being cheaper.

## Scope correction

Only the mandatory outcome-reporting transaction and behaviorally relevant
bounded recall belong to the current player-value proof. Memory archive,
belief conflicts, candidate goals, actor activity, arbitration, structured
generation, and online-model quality are removed from the release value claim.
They remain explicit Preview capabilities for compatibility and experiments,
not recommended defaults. No paid-provider value or quality result is claimed;
deterministic provider cost is exactly zero.

## Release gates

A release may retain the current narrow claim only when:

1. the terminal slice installs and its tests pass on Windows, macOS, and Linux;
2. the real-Sidecar benchmark still recalls the authored preference, reports
   zero deterministic provider calls, and fails closed after mutation starts;
3. 100-hour projected managed storage remains below 50 MiB at the documented
   workload, while operators can configure a lower hard quota;
4. deterministic local P95 remains below 100 ms on at least one documented
   reference machine; and
5. every public value statement links to raw, dated evidence.

A broader “worth the complexity” or Stable claim additionally requires a
separate multi-domain slice and a preregistered, blinded playtest against an
equally persistent authored baseline. At least 20 players, a majority
preference for the Rin condition, no increase in continuity errors, and
measured provider spend are required. Until then, optional cognition features
cannot be promoted into the recommended preset.

# Reference value and evidence

[English](player-value.md) | [简体中文](player-value.zh-CN.md)

Rin `0.7.0` is Preview software. The checked-in examples demonstrate contract
behavior; they do not prove that an AI-controlled character is more enjoyable,
cheaper, or faster than a game-specific rule system.

## What is automated

The engine-neutral Grid and Story adapters run through the same V2 HostKit,
Effect Policy, controller lease, Operation lifecycle, and authoritative Outcome
path:

```bash
go test ./examples/adapters/grid ./examples/adapters/story
```

The Story suite verifies both an internal Agent Runtime and an external MCP
client against the same authoritative scene. It also verifies stale-state
rejection, idempotent replay, cancellation, restart fencing, and a character
boundary denied by Policy.

## What is not claimed

- No checked-in microbenchmark is presented as player value.
- The reference stories are not a usability or narrative-quality study.
- In-memory reference adapters do not prove an engine's threading, save, or
  crash-recovery behavior.
- A model can add expressive variation, but deterministic rules may remain the
  better implementation for small, fixed behaviors.

Before a stable release, measure complete player workflows in each target game:
task completion, false actions, intervention rate, perceived character
consistency, latency, provider cost, save growth, and recovery after process or
network failure. Keep machine-specific benchmark artifacts outside the source
tree unless the methodology and workload are versioned and reproducible.

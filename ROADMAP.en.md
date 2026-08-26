# Rin Roadmap

[English](ROADMAP.en.md) | [简体中文](ROADMAP.md)

Rin is becoming a general game Agent harness: models act freely within explicit
negative constraints, game adapters remain authoritative, and policy plus
operations provide safety, recovery, and auditability.

The current source is `0.7.0` Preview. V2 evolves through intentional breaking
changes; stability takes priority over compatibility with retired interfaces.

## Foundation delivered

- Engine-neutral observations, capabilities, actions, effects, epochs, and Host manifests.
- Deterministic effect-based gameplay policy, confirmation, budgets, and safety kernel.
- Resident Control Daemon, Host leases, exclusive controller leases, and emergency stop.
- Recoverable operations, Host long polling, cancellation, runs, outcomes, and execution proof.
- One action authorization and execution path for external MCP and the Internal Agent.
- Internal persona, memory, skills, structured model decisions, and asynchronous Agent Task API.
- Python, JavaScript, C#, Java, and Lua Control SDKs plus Go HostKit.
- Engine-neutral Grid, Story, and Terminal validation adapters.
- Generic Host contract scaffolds for six languages and local one-command MCP management.
- A single-binary local Console with long-goal entry, a shared default persona,
  and common memory-card management.

## Current gates

1. Run complete build, race, contract, MCP, SDK, installer, and credential scans
   for Rin and the first real game adapter.
2. Fix reproducible findings without adding abstractions that have no consumer.
3. Enter human acceptance for long play, explicitly enabled multiplayer, GUI,
   emergency stop, behavioral naturalness, and model cost.

## Next release phase

- Freeze cross-language fixtures and adapter conformance for `rin.host/v2` and
  `rin.control/v2`.
- Add fault injection for Host disconnects, stale epochs, cancellation races,
  expired confirmations, and unknown outcomes.
- Add observable memory retrieval and compaction metrics without sending the
  entire history to the model.
- Provide standalone Persona, Memory, and Skill provider SPI examples while
  retaining a lightweight built-in local implementation.
- Measure task completion, incorrect authorization, emergency-stop latency,
  token cost, and player intervention in a real game.
- Define the first release candidate and compatibility commitment only after
  human results are stable.

## Later candidates

These remain deferred until the existing execution path passes human validation:

- configurable multi-controller arbitration for one Actor;
- cross-machine Control transport with signed identities, mutual authentication,
  and deployment audit;
- optional vector-memory providers alongside the built-in lightweight store;
- richer macro recovery, child-operation visualization, and cross-adapter evaluations;
- complete reference adapters for RPGs, visual novels, and simulation games;
- acceptance-driven permission editing and behavior explanation in the existing
  Console, without a second control plane.

## Explicit non-goals

- No frame-by-frame model emulation of keyboard or mouse input and no direct
  engine-private API access.
- No execution of model-generated code, shell commands, scripts, or arbitrary
  native calls.
- No Minecraft or other single-game types in the core contract.
- No default cross-save, cross-server, or cross-game character-memory sync.
- No substituting documentation volume, compatibility layers, or abstractions
  for demonstrated player value.

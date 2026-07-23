# Roadmap

[简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

The roadmap tracks reusable runtime capabilities; individual game integration
progress is not part of the public release definition.

## v0.1.0 - Runtime foundation

- [x] Go standard-library HTTP sidecar
- [x] Multi-actor sessions, observations, memories, beliefs, and goals
- [x] Character boundaries and candidate-action allowlists
- [x] Propose/commit separation of world authority
- [x] Tick scheduling and urgent proposals
- [x] Idempotent request IDs, revisions, and stale proposals
- [x] Hash-chained JSONL, atomic snapshots, and restore
- [x] Deterministic offline policy
- [x] macOS, Windows, and Linux CI with zero-CGO builds

## v0.2.0 - Optional model policy

- [x] Standard-library OpenAI-compatible HTTP provider
- [x] Provider timeout, cancellation, retry budget, and circuit breaker
- [x] Strict structured drafts and prompt-injection data isolation
- [x] Asynchronous prefetch job API; the game thread never waits for a model
- [x] Immutable proposal cache keyed by head hash
- [x] Provider contract fixtures with no real API keys

## v0.3.0 - Game adapters

- [x] Ren'Py Python client and offline fallback
- [x] Godot GDScript example
- [x] Unity C# example
- [x] RPG region, visibility, and quest event conventions
- [x] Executable end-to-end protocol compatibility vectors

## v0.4.0 - Structured generation integration

- [x] Generic asynchronous structured Generation Job API
- [x] Request idempotency, semantic cache, cancellation, output-size limit,
  and JSON-object validation
- [x] Ren'Py Generation client and an end-to-end reference integration
- [x] Move game provider credentials into the independent sidecar
- [x] Compose observation, proposal, commit, snapshot, and story generation

## v0.5.0 - Living worlds

- [x] Layered memory summaries and explainable forgetting
- [x] Actor-private knowledge, rumor provenance, and conflicting facts
- [x] Autonomous subgoals and Game Master arbitration
- [x] Multi-agent batching and regional dormancy
- [x] Human-readable debug timeline and decision replay tools

See
[`docs/living-worlds-v0.5-plan.md`](docs/living-worlds-v0.5-plan.md)
for the full protocol, compatibility strategy, phased commits, and acceptance
matrix.

## v0.6.0 - Integration kits

- [x] Dependency-free Python 3.9+ and JavaScript/TypeScript SDKs
- [x] .NET 6, Java 17 with injectable JSON codec, and Lua 5.1 SDKs
- [x] Unified 20-route contract, transport-security constraints, and
  cross-language CI
- [x] Fabric, BepInEx 6, and Luanti NPC example mods
- [ ] Complete manual installation and interaction tests in real
  Fabric/BepInEx/Luanti game versions
- [x] Release the repository, SDKs, and example mods under the MIT License

Every phase keeps one principle: a model may propose intent and expression;
the game engine decides what actually happens.

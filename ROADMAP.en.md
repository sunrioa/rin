# Roadmap

[简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

**Current status:** Rin `0.6.0` is Preview, pre-1.0 software. The numbered
sections below are delivered implementation milestones, not evidence that a
public tag exists for every number. The verified `v0.6.0` tag is created only
after the [release checklist](docs/release-guide.md) passes.

The roadmap tracks reusable runtime capabilities. It does not make an
individual game's integration part of the public runtime definition, and an
unchecked item is not a supported feature.

## Milestone 0.1 - Runtime foundation

- [x] Go standard-library HTTP Sidecar
- [x] Multi-actor Sessions, observations, memories, beliefs, and goals
- [x] Character boundaries and candidate-action allowlists
- [x] Proposal/Commit separation of world authority
- [x] Tick scheduling and urgent proposals
- [x] Request IDs, revisions, stale Proposal protection, and deterministic policy
- [x] Hash-chained JSONL, Snapshot, Restore, and deterministic Replay
- [x] macOS, Windows, and Linux build jobs

## Milestone 0.2 - Optional model policy

- [x] Standard-library OpenAI-compatible HTTP Provider
- [x] Attempt/total timeout, cooperative cancellation, bounded retry, and circuit breaker
- [x] Strict structured Drafts and prompt/game-data isolation
- [x] Asynchronous Proposal Jobs and immutable head-keyed Draft cache
- [x] Provider fixtures without real API keys

## Milestone 0.3 - Game adapters

- [x] Ren'Py Python client and authored offline fallback
- [x] Godot 4 and Unity examples with engine-thread authority
- [x] RPG region, visibility, and quest event conventions
- [x] Executable protocol compatibility fixtures

## Milestone 0.4 - Structured generation

- [x] Generic asynchronous structured Generation Jobs
- [x] Bounded request identity, semantic cache, cancellation, output size, and JSON Object validation
- [x] Ren'Py Generation client and reference composition flow
- [x] Provider credentials remain inside the independent Sidecar
- [x] Generation remains outside Session world authority and Canon

## Milestone 0.5 - Living-world foundations

- [x] Feature-gated layered memory summaries and explainable forgetting
- [x] Actor-private knowledge, sourced conflicting claims, and bounded belief selection
- [x] Game-supplied candidate goals, actor activity, and regional dormancy
- [x] Deterministic advisory arbitration and atomic multi-actor outcome reporting
- [x] Redacted Timeline, revision Replay, and `rin inspect`

## Milestone 0.6 - Preview integration and hardening

- [x] Source-first Python 3.9+, JavaScript/Node 18+, .NET 6+, Java 17+, and Lua 5.1+ clients
- [x] Unified 20-route OpenAPI 3.1 wire schema and generated SDK route inventory
- [x] Fabric, BepInEx 6, and loopback-only Luanti example mods
- [x] Game-authoritative `outcome-reporting-v1`, Proposal Attempt, and Outcome Outbox semantics
- [x] Permanent request/Event ID history and fail-closed uncertain-append reconciliation
- [x] Trusted Restore Binding, Snapshot size limits, and explicit checksum trust boundary
- [x] Lazy Session recovery, range reads, derived checkpoints, and full-history maintenance audit
- [x] Player-text reconstruction and fair bounded memory-summary projection
- [x] Bilingual Changelog, compatibility matrix, migration guide, and release checklist
- [ ] Complete manual installation and interaction tests in real Fabric, BepInEx, and Luanti game versions

## Preview release gates

Before publishing a Preview tag:

- [ ] Required Go, adapter, SDK, contract-generation, and cross-platform build checks pass on the release commit
- [ ] OpenAPI, generated route inventory, protocol prose, and both language sets have no drift
- [ ] A fresh clone can check out, test, and build the proposed tag

These gates describe work to verify for a release commit; this document does
not claim a registry package, automated binary pipeline, streaming Snapshot
transport, cryptographic signing, or post-1.0 stability.

Every milestone keeps one principle: a model may propose intent and expression;
the game engine decides what actually happens.

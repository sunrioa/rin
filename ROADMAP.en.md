# Roadmap

[简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

**Current status:** Rin `0.7.0` is Preview, pre-1.0 software. The numbered
sections below are delivered implementation milestones, not evidence that a
public tag exists for every number. The verified `v0.7.0` tag is created only
after the [release checklist](docs/release-guide.md) passes.

The roadmap tracks reusable runtime capabilities. It does not make an
individual game's integration part of the public runtime definition, and an
unchecked item is not a supported feature.

## Milestone 0.1 - Runtime foundation

- [x] Go standard-library HTTP Sidecar
- [x] Multi-actor Sessions, observations, memories, beliefs, and goals
- [x] Character boundaries and candidate-action allowlists
- [x] Proposal/Action Report separation of world authority
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

- [x] Ren'Py Python client and fail-closed Proposal recovery
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
- [x] Offline deterministic `rin init host` contract skeletons for six custom runtimes,
  Fabric, single-backend BepInEx Mono/IL2CPP, and Luanti, plus sealed
  capability generation, conformance, diagnostics, and Windows gates
- [x] Game-authoritative typed action lifecycle, Proposal Attempt, and Outcome Outbox
- [x] Universal HostKit ports and Coordinator with long-running ActionRun and Epoch reconciliation
- [x] Portable C99 Host reference and shared cross-engine scenario contract
- [x] Preview Unreal Runtime Plugin skeleton with explicit Epoch binding, final
  Game Thread authorization, and Behavior Tree long-action reporting
- [x] Rollback-aware Ren'Py Epoch high-water marks, load/rollback Timeline
  forks, and old-worker invalidation
- [x] Fabric integrated/dedicated logical-server authority, lifecycle Epochs,
  stale-work rejection, and official dedicated-server GameTest
- [x] Unity Domain/Scene authority, durable Active Runs, cancellable NavMesh
  long actions, and late-callback rejection
- [x] Godot 4.6.3 headless authority generations, complete Offer binding,
  Active Run `outcome-unknown` recovery, and Windows CI
- [x] Luanti 5.16.1 real dedicated-server lifecycle, real ModStorage,
  complete Offer binding, Active Run recovery, and Windows CI
- [x] OpenSpiel 2.0.1 real Tic-Tac-Toe, Matrix RPS, and Kuhn poker coverage
  for sequential, simultaneous, chance, and hidden information, with
  SHA-256-pinned wheels on macOS/Linux/Windows
- [x] Permanent request/Event ID history and fail-closed uncertain-append reconciliation
- [x] Trusted Restore Binding, Snapshot size limits, and explicit checksum trust boundary
- [x] Lazy Session recovery, range reads, derived checkpoints, and full-history maintenance audit
- [x] Player-text reconstruction and fair bounded memory-summary projection
- [x] Bilingual Changelog, compatibility matrix, migration guide, and release checklist
- [x] Installable Node.js playable slice, persistent-rule-tree comparison, raw
  benchmark evidence, and Windows/macOS/Linux acceptance job
- [ ] Complete manual interaction tests in real Fabric/BepInEx games and a
  Luanti multiplayer/fault-injection environment

## Milestone 0.7 - Universal-host foundation

- [x] Engine-neutral Go `host` contract covering the host manifest, epochs,
  object references, capability descriptors, offers, invocations, action runs,
  and outcomes
- [x] Self-contained JSON Schema 2020-12 argument/result validation and
  deterministic descriptor digests
- [x] Concurrency-safe capability registry with exact versions, dynamic
  revocation, and final time-of-check/time-of-use validation
- [x] Separate capability discovery, per-decision game authorization, execution
  lifecycle, and persistence guarantees
- [x] Replace the old cross-SDK `HostCapabilities` model with the accurately
  named `HostDurability`, without compatibility aliases
- [x] Schema fuzz, registry race, stale epoch/digest/revocation, and state
  transition tests
- [x] Integrate the Host Contract into cross-language SDKs, generic scaffolds,
  and C99/Unreal reference adapters; remaining real-host acceptance follows the
  evidence matrix
- [x] Define vendor-neutral decision, structured-generation, derived-memory,
  speech, and content-free telemetry ports, including bounded speech caching,
  cancellation, text-only degradation, and playback reporting
- [x] Remove obsolete public Go type names and free-form `DecisionDraft`
  fields without compatibility aliases
- [x] Add bounded `Engine.Close(ctx)` draining for in-flight operations,
  transfers, and checkpoints before CLI Store close
- [x] Add an accelerated-year File Store regression with 1,460 observations,
  365 daily actions, monthly snapshots, restart, history retrieval, byte
  accounting, and archive

## Milestone 0.8 - MCP external control

See the
[MCP external control and Host Control Plane plan](docs/mcp-control-plane-plan.md)
for ordering, authorization boundaries, and acceptance criteria.

- [ ] Host registration, leases, actor/offer read models, and persistent queries
- [ ] Separate `rin-mcp` stdio and loopback Streamable HTTP gateway
- [ ] Conversation, rejectable directives, exact offer execution, and operation queries
- [ ] Pairing, scopes, revocation, high-risk confirmation, idempotency, and restart reconciliation
- [ ] One real server game proving MCP and in-game entry points share one execution service
- [ ] Cross-language Host Control clients, interoperability, security, and fault-injection acceptance

This milestone never permits MCP to bypass host-authored offers, epochs, TOCTOU
checks, engine threading, or game authorization.

## Preview release gates

Before publishing a Preview tag:

- [ ] Required Go, adapter, SDK, contract-generation, and cross-platform build checks pass on the release commit
- [ ] OpenAPI, generated route inventory, protocol prose, and both language sets have no drift
- [ ] A fresh clone can check out, test, and build the proposed tag
- [ ] Player-value claims remain inside the measured scope and satisfy the
  [evidence gates](docs/player-value.md)

These gates describe work to verify for a release commit; this document does
not claim a registry package, automated binary pipeline, cryptographic
signing, or post-1.0 stability. Inline Snapshot remains non-streaming; bounded
Session Transfer is a separate supported complete-lineage path.

## Next remediation priorities

- [x] Implement bounded-memory, verifiable, atomically published complete
  lineage export/import according to the
  [Scalable Session Transfer design](docs/session-transfer.md), removing the
  lifetime cliff where Identifier History growth makes Snapshot, Replay, and
  Restore unavailable.
- [x] Do not mark transfer supported before over-16-MiB end-to-end,
  cancellation, corruption, and crash-recovery tests pass. Raising the request
  body limit alone is not a substitute for streaming transfer.
- [x] Implement a Windows data-directory exclusive lock plus real Sidecar
  persistence, restart, and lock-contention tests. Windows support is a project
  constraint; cross-compilation alone is not runtime support.
- [x] Remove unmeasured optional cognition features from the release value
  claim; the single-preference slice reaches parity with a much smaller
  persistent rule tree and does not justify a broader “worth it” claim.

Every milestone keeps one principle: a model may propose intent and expression;
the game engine decides what actually happens.

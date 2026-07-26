# Changelog

[简体中文](CHANGELOG.zh-CN.md) | [English](CHANGELOG.md)

This changelog records repository-level changes. Rin `0.7.0` is a Preview
release: it is pre-1.0, and compatibility is documented rather than guaranteed
across every future minor release.

## Unreleased

### Added

- An engine-neutral Go `host` contract with validated host manifests,
  authoritative epochs, opaque object references, versioned capabilities,
  game-bound action offers, invocations, action-run states, and outcomes.
- A concurrency-safe capability registry with root-closed JSON Schema 2020-12
  inputs/outputs, deterministic descriptor digests, dynamic revocation, and a
  final time-of-check/time-of-use authorization pass.
- Fuzz, race, stale-epoch, expiry, digest-drift, revocation, durability, and
  action-transition tests for the contract.

### Changed

- The wire contract is now `rin.protocol/v2`. Decision Windows, fully bound
  Action Offers, Epochs, typed Invocation/Run/Outcome reports, and
  `/v2/action/report[-batch]` replace v1 ActionSpec/Commit semantics.
- The v1 wire, reducer compatibility branches, old recovery example, obsolete
  semantic-baseline/migration documents, and compatibility aliases were
  deleted. Development users must start a new lineage or use explicit
  export/import.
- Unity now exposes a compact `IRinUnityHost` boundary, preserves arbitrary
  JSON arguments, and uses a durable Pending Turn plus exact report outbox.
- Runtime/server code remains standard-library-only, while the separate Host
  Contract uses the maintained `santhosh-tekuri/jsonschema` validator. License
  metadata is recorded in `THIRD-PARTY-NOTICES.md`.
- Capability discovery is explicitly not action authority: models select only
  arguments and targets already bound by a game-authored `ActionOffer`.
- The misleading `HostCapabilities`/`HostProfile` SDK model is replaced by
  `HostDurability`/`HostDurabilityProfile` in JavaScript, C#, Java, embedded
  scaffold assets, and reference Mods. Old names, error codes, and documentation
  paths are removed rather than retained as compatibility aliases.

## [0.6.0] - 2026-07-24 - Preview

The `v0.6.0` tag is created from the verified main branch only after the
release checklist passes. See the [release guide](docs/release-guide.md).

### Added

- Versioned host-capability validation and shared Pending Turn workflow
  coordinators for JavaScript, C#, and Java.
- A pinned, installable Fabric 1.21.1 server Mod with stable Saved Data
  identity, restartable Pending Turn/Outbox state, and Linux/Windows builds.
- A game-authoritative Observation -> Proposal -> apply/reject -> Commit
  lifecycle, with `outcome-reporting-v1` for late outcome merging and durable
  game-side Outbox recovery.
- Durable, lineage-wide request and Event ID history, including exact retry
  results and fail-closed recovery from uncertain Store appends.
- Feature-gated memory archives, conflicting actor-local beliefs, candidate
  goals, actor activity, world arbitration, and atomic batch outcome reporting.
- Timeline, revision Replay, internal replay checkpoints, `rin inspect`, and
  explicit full-history verification through `Engine.VerifyAll()`.
- Asynchronous Proposal and structured Generation Jobs with bounded queues,
  retention, cancellation, provider retries, and circuit breaking.
- Source-first Python, JavaScript, C#, Java, and Lua clients, plus Ren'Py,
  Godot, Unity, Fabric, BepInEx, and Luanti integration examples.
- An offline `rin init host` generator for engine-neutral Go, JavaScript,
  Python, C#, Java, and Lua contract skeletons plus standalone Fabric, single-backend
  BepInEx Mono/IL2CPP, and Luanti projects. `rin add skill`,
  `rin conformance host`, and `rin doctor host` provide sealed capability
  generation, contract checks, and cross-platform runtime diagnostics.
- A type-checked universal HostKit reference with explicit transport,
  authority-thread, state, identity, observation, capability, execution, and
  artifact ports; its Coordinator persists before network, validates exact
  offers, tracks long-running actions, retries an exact Outbox, and reconciles
  stale Epochs.
- An OpenAPI 3.1 wire schema at [`api/openapi.json`](api/openapi.json) and a
  [compatibility matrix](docs/compatibility.md).
- Bounded-frame NDJSON Session Transfer with immutable export boundaries,
  per-event and stream checksums, trusted Binding headers, same-root staged
  import, atomic publication, terminal error frames, and caller-owned
  JavaScript/C# stream helpers. End-to-end coverage moves a lineage larger
  than 16 MiB, replays it, and resumes mutation.
- Authenticated Session stats, archive, and fail-closed deletion with permanent
  ID tombstones, plus configurable soft/hard managed-storage quotas. File Store
  lifecycle recovery is covered on Linux, macOS, and Windows.
- Separate liveness/readiness probes, authenticated bounded diagnostics,
  dependency-free Prometheus metrics, and content-free structured request
  correlation.
- An installable Node.js terminal-story vertical slice with a durable
  JavaScript workflow, cross-platform acceptance job, raw benchmark evidence,
  and an equally persistent rule-tree comparison.

### Changed

- New Sessions must use the `outcome-reporting-v1` safe baseline. Existing
  histories and exact Create retries without it retain their historical
  reducer and Commit semantics.
- Restore now requires `expected_binding` from the running game's trusted
  content manifest. It must match both the imported Snapshot and any existing
  target Session.
- `rin.reducer-projection/v2` reconstructs Proposal presentation from
  game-authored action descriptions and uses fair bounded memory-summary
  sampling. It does not rewrite authoritative event bytes.
- Bounded recalled-memory tags now influence deterministic allowlisted action
  selection below Goal preferences, making offline recall behaviorally useful
  without exposing private memory text.
- The bundled File Store lazily loads Sessions, uses a revision index and
  derived checkpoints, retains the event log indefinitely, and supports only
  local filesystems with the documented locking and sync guarantees.

### Hardened

- Host integrations now declare an explicit `advisory`,
  `idempotent-action`, or `transactional-action` durability profile. The
  repository records non-increasing example-code budgets so protocol workflow
  logic cannot silently grow further inside game adapters.
- Java centralizes Proposal freshness and terminal Commit-to-safe-Observe
  recovery; the Fabric adapter remains honestly `advisory` because Saved Data
  dirty marking is not a synchronous transaction boundary.
- Inline Snapshot compact JSON is capped at 16 MiB; default request and bundled
  client response limits are 32 MiB. Oversized state is rejected, never
  truncated.
- Snapshot and checkpoint hashes are documented as checksums, not signatures
  or provenance proof. Event hashes are likewise unkeyed and do not prevent a
  writer from rebuilding a complete chain.
- Provider prompts, credentials, and raw HTTP bodies are excluded from errors,
  logs, and durable Session state. Validated Generation content remains bounded
  process-local Job/cache data until returned to the caller.
- Public HTTP JSON integers use the exact interoperable range
  `-9007199254740991` through `9007199254740991`, with narrower non-negative
  constraints where the schema specifies them.
- Commit and Batch Commit item `accepted` fields must be present explicitly;
  omission is not interpreted as `false`.
- Raw game-facing HTTP request bodies and successful Provider JSON responses
  are strictly checked before decoding; invalid UTF-8 and unpaired Unicode
  surrogates are rejected. Non-2xx Provider bodies are used only for bounded
  error classification, never as Generation content or Session state.

### Compatibility notes

- `rin.protocol/v1` remains the wire identifier, but Preview v1 has gained
  additive response fields, feature-gated semantics, and stricter request
  validation. Pin the Sidecar, client source, and conformance inventory to one
  repository revision.
- Requests reject unknown fields. Clients must tolerate unknown additive
  response fields.
- HTTP failures use the error envelope. A Proposal or Generation Job can
  instead reach an HTTP `200` terminal state whose `data.error` describes the
  asynchronous operation failure.
- SDKs remain source-first and are not published to language registries.

### Known limitations

- Rin is Preview software and does not yet provide a post-1.0 compatibility or
  deprecation guarantee.
- Complete inline Snapshots remain non-streaming and capped at 16 MiB; Session
  Transfer is the supported complete-lineage migration/backup path.
- The bundled File Store supports local `darwin`, `linux`, and `windows`
  filesystems and is not supported on network, FUSE, or cloud-synchronized
  filesystems.
- Event and Snapshot hashes do not authenticate an adversarially rewritten
  history.
- Real-version manual installation and interaction checks for the Fabric,
  BepInEx, and Luanti examples remain release follow-up work.

## Earlier implementation milestones

The repository history contains implementation milestones named 0.1 through
0.5. They were development phases, not a promise that corresponding public
release tags exist. Their delivered capabilities are summarized in the
[roadmap](ROADMAP.en.md).

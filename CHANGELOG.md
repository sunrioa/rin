# Changelog

[简体中文](CHANGELOG.zh-CN.md) | [English](CHANGELOG.md)

This changelog records repository-level changes. Rin `0.6.0` is a Preview
release: it is pre-1.0, and compatibility is documented rather than guaranteed
across every future minor release.

## [0.6.0] - 2026-07-24 - Preview

The `v0.6.0` tag is created from the verified main branch only after the
release checklist passes. See the [release guide](docs/release-guide.md).

### Added

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
- An OpenAPI 3.1 wire schema at [`api/openapi.json`](api/openapi.json), a
  [compatibility matrix](docs/compatibility.md), and a
  [v0.6 migration guide](docs/migration-v0.6.md).

### Changed

- New Sessions should opt into `outcome-reporting-v1`. Existing Sessions
  without it retain their historical reducer and Commit semantics.
- Restore now requires `expected_binding` from the running game's trusted
  content manifest. It must match both the imported Snapshot and any existing
  target Session.
- `rin.reducer-projection/v2` reconstructs Proposal presentation from
  game-authored action descriptions and uses fair bounded memory-summary
  sampling. It does not rewrite authoritative event bytes.
- The bundled File Store lazily loads Sessions, uses a revision index and
  derived checkpoints, retains the event log indefinitely, and supports only
  local filesystems with the documented locking and sync guarantees.

### Hardened

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
- Complete inline Snapshots have no streaming transport.
- The bundled File Store supports `darwin` and `linux` only and is not supported
  on network, FUSE, or cloud-synchronized filesystems.
- Event and Snapshot hashes do not authenticate an adversarially rewritten
  history.
- Real-version manual installation and interaction checks for the Fabric,
  BepInEx, and Luanti examples remain release follow-up work.

## Earlier implementation milestones

The repository history contains implementation milestones named 0.1 through
0.5. They were development phases, not a promise that corresponding public
release tags exist. Their delivered capabilities are summarized in the
[roadmap](ROADMAP.en.md).

# Changelog

[简体中文](CHANGELOG.zh-CN.md) | [English](CHANGELOG.md)

The current source is `0.7.0` Preview. Breaking changes are allowed before 1.0;
pin an exact commit or tag for every distribution.

## Unreleased: Harness V2

### Added

- Engine-neutral `rin.host/v2` contracts for observations, capability specs,
  action requests, bound actions, effects, runs, and outcomes.
- `rin.control/v2` with a resident Control Daemon, Host leases, controller
  leases, emergency stop, action gateway, policy, operations, confirmation,
  cancellation, and result reconciliation.
- Optional Internal Agent Runtime with persona, memory, skills, structured model
  decisions, asynchronous Agent Task API, and macro parent-child loops.
- Thin MCP 2026-07-28 proxy plus local install, status, update, and uninstall
  commands for Codex, Claude Code, and OpenClaw.
- Python, JavaScript, C#, Java, and Lua Control V2 clients plus Go HostKit.
- Grid, Story, and Terminal V2 validation adapters.
- Generic Host contract scaffolding for Go, JavaScript, Python, C#, Java, and Lua.
- Optional OpenAI-compatible semantic memory recall with explicit domain
  egress, rebuildable SQLite vector projections, and offline fallback.

### Changed

- Internal model requests now use a deterministic static prompt prefix and map
  compatible provider cache-token aliases into the existing task timeline.
- Models now choose capabilities, arguments, and targets inside a Host-published
  catalog and trusted observation rather than selecting a few prebound options.
- Authorization now evaluates Host-bound effects, ownership, scope, risk, rules,
  and budgets rather than capability names alone.
- Internal models and external MCP share controller lease, action gateway,
  policy, operation, and Host outcome semantics.
- MCP processes are stateless STDIO proxies. The resident `rin-control` owns the
  port, persistent state, and fixed principal.
- Hosts can keep an actorless world discoverable to declared read Principals
  without granting control or execution authority.
- SDKs converge on source-first Control V2 clients with OpenAPI as the exact HTTP contract.
- Host scaffolding generates only a `custom` contract skeleton and no longer
  claims to generate a real engine project.
- Agent context assembly, task lifecycle, plan/decision orchestration,
  action/operation coordination, and signal wake scheduling are now focused
  package-private components without changing model, planner, operation, or
  task-timeline semantics.

### Security

- Controllers cannot declare effects, ownership, risk, authorization, or success.
- The built-in safety kernel denies arbitrary code, file access, native calls,
  authority forgery, secret exposure, and unknown effect/scope/ownership.
- The Control Daemon accepts loopback only and requires a token of at least 32
  bytes. Agent API tokens and model API keys are separate.
- The Internal Agent places persona, memory, skills, observations, and player
  text under `untrusted_context`, then validates closed-schema output against
  the machine-selected allowed set.
- Operations distinguish queue, acceptance, running, success, failure, stale,
  and unknown result. Only a Host outcome sets `execution_confirmed=true`.

### Removed

- Removed runtimes, planner DSLs, compatibility branches, and migration tools
  with no V2 consumer.
- Removed Ren'Py, Godot, Unity, Unreal, Fabric, BepInEx, Luanti, and native
  examples that copied retired contracts, along with their toolchains and
  misleading conformance claims.
- Removed engine-specific templates. Concrete game integrations belong in
  independent adapter repositories.
- Removed documents that described deleted architecture, migration, or
  duplicated workflows.
- Removed the retired file memory provider and initial `memory.json` migration
  path. `memory.db` is the only online store in the Rin Memory domain; JSONL is
  an explicit exchange format only.

### Acceptance status

- The Rin Go core, race suite, OpenAPI contracts, five language SDKs, and three
  V2 examples have automated gates.
- Automated contract and full-process regression passes for the Minecraft and
  visual-novel adapters. Installation, save/load, forced termination,
  multiplayer authority, emergency stop, UI, long-play, and character
  naturalness still require human acceptance.

## [0.6.0] - 2026-07-24 - Preview

This entry records the behavior of the `v0.6.0` tag. It describes the retired
V1 architecture and is retained as release history, not as current V2 usage
documentation.

### Added

- A game-authoritative Observation -> Proposal -> apply/reject -> Commit
  lifecycle, including late outcome merging and durable game-side Outbox
  recovery.
- Durable, lineage-wide request and event ID history, exact retry results,
  revision replay, internal replay checkpoints, `rin inspect`, and explicit
  full-history verification.
- Feature-gated memory archives, actor-local beliefs and goals, actor activity,
  world arbitration, and atomic batch outcome reporting.
- Asynchronous Proposal and structured Generation Jobs with bounded queues,
  retention, cancellation, provider retries, and circuit breaking.
- Source-first Python, JavaScript, C#, Java, and Lua clients; an OpenAPI 3.1
  wire schema; and engine integration examples available at that release.

### Changed

- New Sessions could opt into late outcome reporting while existing Sessions
  retained their historical reducer and Commit semantics.
- Restore required an `expected_binding` from the running game's trusted
  content manifest and checked it against both the imported Snapshot and an
  existing target Session.
- `rin.reducer-projection/v2` reconstructed Proposal presentation without
  rewriting authoritative event bytes.
- The bundled File Store added lazy Session loading, a revision index, and
  derived checkpoints while retaining the event log indefinitely.

### Security

- Inline Snapshot JSON was capped at 16 MiB; default request and bundled client
  response bodies were capped at 32 MiB. Oversized input was rejected rather
  than truncated.
- Provider prompts, credentials, and raw HTTP bodies were excluded from errors,
  logs, and durable Session state.
- Public HTTP JSON integers used the exact interoperable range, Commit
  acceptance required an explicit field, and malformed UTF-8 or Unicode in
  game-facing and successful provider JSON was rejected before decoding.
- Snapshot, checkpoint, and event hashes were documented as unkeyed checksums,
  not signatures or proof against an adversarial history rewrite.

### Compatibility notes

- This was a pre-1.0 Preview contract. Distributions needed to pin the Sidecar,
  client source, and conformance inventory to the same repository revision.
- Requests rejected unknown fields while clients were expected to tolerate
  additive response fields. SDKs were source-first and were not published to
  language registries.
- Complete Snapshots had no streaming transport. The bundled File Store was
  supported only on local `darwin` and `linux` filesystems.

## Earlier implementation milestones

Repository history also contains milestones named 0.1 through 0.5. They were
development phases, not evidence that corresponding public release tags exist.

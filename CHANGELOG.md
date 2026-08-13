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

- Models now choose capabilities, arguments, and targets inside a Host-published
  catalog and trusted observation rather than selecting a few prebound options.
- Authorization now evaluates Host-bound effects, ownership, scope, risk, rules,
  and budgets rather than capability names alone.
- Internal models and external MCP share controller lease, action gateway,
  policy, operation, and Host outcome semantics.
- MCP processes are stateless STDIO proxies. The resident `rin-control` owns the
  port, persistent state, and fixed principal.
- SDKs converge on source-first Control V2 clients with OpenAPI as the exact HTTP contract.
- Host scaffolding generates only a `custom` contract skeleton and no longer
  claims to generate a real engine project.

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

### Acceptance status

- The Rin Go core, race suite, OpenAPI contracts, five language SDKs, and three
  V2 examples have automated gates.
- A real game adapter must still complete installation, save/load, forced
  termination, multiplayer authority, emergency stop, UI, long-play, and
  character-naturalness acceptance.

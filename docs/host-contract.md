# Host Contract

[English](host-contract.md) | [简体中文](host-contract.zh-CN.md)

The `host` Go package is Rin's engine-neutral boundary for describing a game
host and the operations it can safely expose. Protocol v2 reuses its exact
Epoch, capability, offer, invocation, run, and outcome shapes on the HTTP
boundary; the game still owns local registration and execution.

## Why this boundary exists

A universal game integration cannot pretend that every engine shares one
world, tick, object model, or navigation system. It can standardize the facts
needed to make a safe decision:

- `HostManifest` declares authority, deployment, control, clock and decision
  modes, actor concurrency, and persistence guarantees.
- `CapabilityDescriptor` gives one namespaced capability an exact semantic
  version, bounded JSON Schema input/output, effect, execution shape, risk,
  permissions, timeout, cancellation, and durability requirements.
- `ActionOffer` is a game-authored candidate with arguments and targets
  already bound to an authoritative `Epoch`.
- `ActionInvocation` carries the accepted offer to the local executor and
  receives a final time-of-check/time-of-use authorization immediately before
  authority-thread dispatch.
- `ActionRun` and `ActionOutcome` distinguish queueing, execution, uncertain
  recovery, and terminal evidence. Cancellation is not rollback; a reversible
  capability still needs a separate compensating operation.

This split follows the useful common ground in Unity ML-Agents' action
contracts, Unreal Gameplay Ability activation, ROS 2 Actions' long-running
goal lifecycle, and OpenSpiel/PettingZoo's different decision-time models.
Rin does not import those frameworks or reproduce their engine-specific world
models. The [OpenSpiel validation](open-spiel-validation.md) now exercises
sequential, simultaneous, chance, and hidden-information mappings on real game
states.

## Discovery is not authority

`Registry.Snapshot` answers “what can this host implementation do?” It never
answers “what may this actor do now?”

For every decision the game creates bounded `ActionOffer` values. A policy
selects an `offer_id`; it does not invent a method name, arbitrary JSON
arguments, object pointer, console command, shell command, or generated code.
The host then validates:

1. the exact capability ID and semantic version still exist;
2. the descriptor SHA-256 digest has not changed;
3. arguments match the root-closed JSON Schema;
4. the offer has not expired and its Epoch still matches;
5. the capability has not been dynamically revoked;
6. the same checks still hold immediately before authority-thread execution.

The registry can therefore be shared by Ren'Py labels, Unity components,
Unreal abilities, Godot nodes, server Mods, Web games, and custom engines
without granting any one of them special semantics.

## Epoch and object references

`Epoch` contains stable Session and World IDs plus three positive JSON-safe
generations:

- `host` changes when the owning host process or authoritative instance is
  replaced;
- `world` changes on a scene, map, level, shard, or equivalent world reload;
- `timeline` changes on save forks, rollback, rewind, or authoritative branch.

The Epoch Session ID must equal the containing request and Session state.
Observation, decision, invocation, and outcome boundaries reject a nested
Epoch from another Session.

These values are not render frames, physics frames, simulation steps, or wall
clock time. `HostRef` is opaque outside its adapter. Only that adapter may
resolve it on the engine's authority thread; an ephemeral reference must not be
persisted.

## Schema and descriptor rules

The package uses self-contained JSON Schema 2020-12 documents. A schema must:

- be at most 64 KiB and have an object root;
- declare the exact 2020-12 `$schema` URI and `type: "object"`;
- explicitly set `additionalProperties: false`;
- contain no duplicate JSON property names or externally loaded references.

Nested object schemas must close their own properties when that is required;
the contract enforces this rule at the root and does not rewrite authored
subschemas.

Canonical compact JSON is hashed with SHA-256. Capability descriptors bind the
schema hashes and operational limits into a second deterministic digest.
The implementation uses the maintained
[`santhosh-tekuri/jsonschema`](https://github.com/santhosh-tekuri/jsonschema)
validator instead of maintaining a partial schema engine.

## Durability is a separate axis

The existing [host durability profiles](host-durability.md) describe
crash/retry durability only:

- `advisory`: no world-mutation recovery claim;
- `idempotent-action`: durable pending work/outbox and idempotent application;
- `transactional-action`: effect and outcome outbox can be published atomically.

They do not enumerate gameplay capabilities. A descriptor states the minimum
durability it needs, and the registry rejects registration when the host cannot
provide it. Risk, permissions, execution mode, cancellation, and reversibility
remain independent axes.

## Delivered boundary

The Go package provides validation, deterministic sealing,
concurrent registration/discovery, dynamic revocation, offer/invocation/output
checks, action-state transitions, and race/fuzz coverage. Protocol v2 and the
language SDKs carry the typed lifecycle. The
[Universal Host SDK](host-sdk.md) adds the eight engine-facing ports, durable
Pending Decision/ActionRun/Outbox state, authority dispatch, exact retry, and
Epoch reconciliation. Generated Host projects are covered separately by the
[Host scaffolding guide](host-scaffolding.md).

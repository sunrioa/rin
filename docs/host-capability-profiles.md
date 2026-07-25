# Host capability profiles

[English](host-capability-profiles.md) | [简体中文](host-capability-profiles.zh-CN.md)

Rin coordinates a distributed workflow across a game process and the Sidecar,
but only the game owns world state. An integration must declare one of the
profiles below for each class of action. The profile is a statement about the
host's real persistence and apply boundary, not about which Rin API methods it
calls.

This document describes Rin `0.6.0` Preview. It does not change the
`rin.protocol/v1` wire schema.

## Profiles

| Profile | Required host guarantee | Permitted action class |
| --- | --- | --- |
| `advisory` | Stable identity is recommended, but no crash-atomic apply boundary is claimed. Pending work may be persisted only eventually or remain in memory. | Display-only suggestions, dialogue, reversible effects, or effects whose repetition/loss is acceptable to the game. |
| `idempotent-action` | A complete Pending Turn is durable before network submission. Every effect accepts a stable operation ID and the game can prove that replaying that ID does not apply the effect twice. The report Outbox is durable. | Durable effects implemented by a genuinely idempotent game API. Recovery is at-least-once; the operation ID makes repeats harmless. |
| `transactional-action` | A complete Pending Turn is durable before network submission. One host transaction atomically applies the effect, records the applied operation, enqueues the exact Outcome report, and removes the Pending Turn. The Outbox is durable. | Durable game effects that participate in the same authoritative transaction. This is the strongest Rin workflow profile. |

An integration may use different profiles for different actions. For example,
dialogue can be `advisory` while a quest engine with an operation-keyed command
can be `idempotent-action`. Capability negotiation must fail closed: an action
that requires a stronger profile must not be offered when the host cannot
provide it.

`setDirty()`, a save-slot write API, or a key/value setter does not by itself
prove that bytes reached durable storage before the next HTTP request. Likewise,
writing a marker after mutating the world is not an atomic transaction. Such
hosts remain `advisory` unless they expose either a documented synchronous
durability boundary or an operation-keyed idempotent apply API.

## Versioned capability record

SDKs and adapters use this logical record:

```text
HostCapabilities {
  version: 1
  profile: advisory | idempotent-action | transactional-action
  stable_identity: boolean
  durable_before_network: boolean
  durable_outbox: boolean
  idempotent_apply: boolean
  atomic_apply_and_outbox: boolean
}
```

The SDK validates combinations instead of trusting the profile label:

- `advisory` makes no durability claim.
- `idempotent-action` requires stable identity, durable-before-network state,
  a durable Outbox, and idempotent apply.
- `transactional-action` requires stable identity, durable-before-network
  state, a durable Outbox, and atomic apply/marker/enqueue/removal.
- `atomic_apply_and_outbox` and `idempotent_apply` are mutually independent;
  a host may truthfully support both.

Capabilities are local host facts. They are not sent to the model and do not
grant authority. Candidate action allowlists remain game-authored.

## Stable identity

A stable Session identity must derive from durable game identity such as a
world UUID plus save slot and actor key. It must not derive from process start
time, a newly generated startup UUID, frame count, a machine path, or a bearer
token. The host stores the final protocol-safe identity in the save when one is
not already available.

Changing content binding or intentionally starting a new campaign may create a
new identity. Restarting the same world must not.

## Recovery obligations

Before submission, persist the complete typed Propose request, its request ID,
operation ID, and sequence. Persist the returned Job ID immediately after a
successful `202`. On startup and before any new turn:

1. drain retained Outcome reports;
2. resume the retained Pending Turn with the same identities;
3. fail closed while submission or polling has an unknown outcome;
4. settle through the host boundary permitted by the declared profile;
5. start new work only after retained work is resolved.

The coordinator owns this protocol-generic state machine. The host owns stable
storage, the authoritative apply callback, engine-thread dispatch, and action
validation.

## Current reference status

The checked-in Fabric, BepInEx, Luanti, Godot, and Unity examples declare
`advisory`. Fabric and BepInEx now have stable identity and restartable bounded
workflow state; the other references still complete their dedicated
persistence work in later phases. Restart recovery alone does not establish a
durable-before-network or atomic apply boundary.

Fabric Saved Data is designed for cross-session persistence, but marking it
dirty schedules later saving; that alone is not a durable-before-network
barrier. Luanti ModStorage persistence is tied to `map_save_interval` and may
use JSON or SQLite, so a setter is likewise not a synchronous crash boundary.
BepInEx supports games with materially different Mono, IL2CPP, and target
framework constraints; capability claims must be made by the game-specific
plugin, not by BepInEx as a whole. The reference state file uses a
flush-and-replace sequence, but cannot atomically include an arbitrary game's
effect and must not be promoted beyond `advisory` on that basis.

## Review checklist

- Is Session identity stable across restart and independent of filesystem path
  syntax on Windows?
- Can the host prove the Pending Turn is durable before POST?
- Can an interrupted apply be retried without duplicating the effect?
- Are effect, marker, Outbox enqueue, and Pending Turn removal truly atomic?
- Is Outbox acknowledgement durable before entry removal?
- Do shutdown and save hooks wait only within a bounded deadline?
- Are model text and private audit fields excluded from action dispatch?
- Does documentation name the actual profile rather than imply “exactly once”?

Primary host references:

- [Fabric Saved Data](https://docs.fabricmc.net/develop/serialization/saved-data)
- [Luanti ModStorage](https://docs.luanti.org/for-creators/api/classes/modstorage/)
- [BepInEx 6 plugin project guide](https://docs.bepinex.dev/v6.0.0-pre.1/articles/dev_guide/plugin_tutorial/2_plugin_start.html)

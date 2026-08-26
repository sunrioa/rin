# Game adapter guide

[English](game-adapters.md) | [简体中文](game-adapters.zh-CN.md)

Game-specific integration lives in the adapter. Rin does not require ECS,
behavior trees, a particular scripting language, or a network topology. The
complete adapter responsibilities are defined by the required interface below.

## Required boundary

Go HostKit expresses the complete interface below. Other languages should
preserve the same semantics:

```text
Manifest()
Snapshot(target)
Observe(query)
ListCapabilities()
Bind(target, request)
Preview(target, request, binding)
Execute(operation)
Cancel(operation)
Verify(operation)
PolicyFacts(target)
```

`Snapshot`, `Observe`, `Bind`, `Preview`, `Execute`, `Cancel`, and `Verify` may
touch game state and must run through the game's server/main/authority-thread
dispatcher.

## Identity mapping

Map stable identities from the game save:

- `host_id`: adapter or service identity, not an ephemeral port;
- `world_id`: stable current save/world identity;
- `actor_id`: stable character identity within that save;
- `Epoch.host`: increments when the authority instance changes;
- `Epoch.world`: increments on scene, dimension, or map reload;
- `Epoch.timeline`: increments on load, rollback, or branching;
- `observation_sequence`: monotonically increases inside an epoch.

Do not use an object address, transient runtime ID, display name, or position as
a durable identity.

## Observation design

A capable model needs freedom, not a complete world dump. Prefer publishing:

- the Actor's state, location, current activity, and danger;
- bounded nearby entities, resources, and interactable objects;
- ownership and scope for items, targets, and regions;
- current long task, failure reason, and cancellation state;
- facts relevant to the current task rather than all history.

Paginate large sets. Expose objects through opaque `HostRef` values. Never place
filesystem paths, object pointers, private NBT/component content, tokens, or
server secrets in observations.

## Capability design

Capabilities express stable, verifiable game semantics, for example:

```text
navigation.move_to
resource.harvest
inventory.transfer
crafting.craft
combat.attack
dialogue.say
quest.accept
building.place_batch
```

The harness should tell a model which effects are forbidden rather than teach
every task as a fixed flow. Therefore:

- expose a small composable set of atomic capabilities;
- constrain arguments and targets through schemas and HostRefs;
- express assets, regions, risk, and quantity through effects;
- let the model replan from outcomes;
- provide macros only for frequent, deterministic work that saves substantial
  decision overhead.

Do not make one capability per material, enemy, or narrative branch. Do not
offer `execute_anything`, arbitrary class names, arbitrary scripts, or raw engine
objects.

## Atomic capabilities and real-time controllers

One semantic action may span many ticks. After a model submits
`navigation.move_to`, the adapter's navigation controller handles pathfinding,
avoidance, and danger checks each tick; the model need not stream movement keys.

A long-running action should:

- expose progress with monotonic `progress_seq`;
- stop when a target disappears, the epoch changes, budget expires, or danger rises;
- implement its declared cooperative or preemptive cancellation;
- return actual quantity, location, targets, and failure code;
- never present “started” as “completed.”

## Binding and TOCTOU

`Bind` resolves controller intent without mutating the world. `Preview` derives
real effects from resolved objects. Immediately before `Execute`, check again:

- capability publication and digest;
- controller, Actor, epoch, and observation;
- target existence, allowed region, and distance;
- ownership, container permission, tools, resources, cooldown, and game mode;
- that policy authorized the same effect digest;
- operation idempotency or safe exact retry.

Return a structured error and let the controller replan from a new observation.
Do not silently replace targets or broaden scope.

## Effects and local rules

An adapter maps game rules to standard fields. Harvesting a block might produce:

```text
kind=world.resource
operation=delete
ownership=unowned
scope=world:wilderness
quantity=1
tags=[resource.common, tool.pickaxe]
risk=low
reversible=false
```

Administrators configure known kinds/scopes, rules, and budgets. Unknown third-
party content should remain unknown and denied, or be classified by an explicit
administrator allowlist or game tag. Never infer safety from a name suffix alone.

## Multiplayer and maximum authority

Public servers may support autonomous NPCs, but should default to disabled. The
adapter needs in-game configuration for:

- which players bind or control each Actor;
- allowed dimensions, regions, containers, and assets;
- effects that auto-execute, require confirmation, or remain forbidden;
- action, block/item quantity, radius, and duration budgets;
- exact allowlists for commands, command blocks, or administrator capabilities.

The highest tier still exposes only registered, bindable, auditable
capabilities. If console commands are a real product requirement, represent
them as separate critical capabilities with enum or strict-schema arguments,
system ownership, and owner/admin confirmation. Never expose an arbitrary
command string or permission bypass.

## Internal and external control

An adapter publishes `decision_authority`:

- `source=internal`: Rin Internal Agent uses game-configured persona and memory;
- `source=external`: an external Agent with the matching principal controls via MCP;
- `persona_mode=character-bound`: the external Agent preserves the game role;
- `persona_mode=agent-avatar`: the character represents the external Agent persona.

Only one controller lease exists at a time. External MCP must not depend on an
internal thinker; macros and real-time controllers are deterministic adapter
capabilities, not an internal model. An authority change increments its
revision, ends the old lease, and fences unaccepted old actions.

A game mod does not implement its own MCP server. It implements the Host Control
client and adapter; every compatible game shares one `rin-mcp` installation.

## Persistence

At minimum, the game save owns:

- stable World/Actor IDs and epoch high-water marks;
- decision authority, permission configuration, and emergency stop;
- recovery state for accepted unfinished operations;
- applied-operation markers and pending outcome outbox;
- game-owned long tasks and world canon.

Do not save threads, sockets, futures, callbacks, engine objects, or API keys.
After reconnect, register the Host, republish its read model, and reconcile the
same operation IDs.

## Validation

First pass the contract with Grid/Story adapters, then cover in the real game:

- main-thread and server authority;
- normal save/load, world changes, disconnect, and forced termination;
- redelivery without duplicated effects;
- emergency-stop and cancellation latency;
- multiplayer permissions and player assets;
- authoritative outcomes for long tasks;
- UI without render or tick blocking.

See [integration acceptance](host-integration-validation.md) for the full gate.

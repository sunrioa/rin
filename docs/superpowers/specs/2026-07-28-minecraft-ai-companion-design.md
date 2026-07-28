# Minecraft AI Companion Design

## Goal

Build an installable Fabric mod for the user's PCL2 Minecraft instance that adds
one persistent, player-shaped AI companion. The companion can chat through the
normal chat bar, follow the owner, explore with or without the owner, survive,
collect resources, craft, fight, and build while obeying Minecraft survival
rules.

The implementation uses Rin for bounded decisions, memory, asynchronous model
work, restart recovery, and action reporting. Minecraft remains authoritative
for entities, navigation, inventory, recipes, blocks, damage, permissions, and
world state.

## Verified target environment

- Launcher root: `E:\ruanjian\pcl`
- Game root: `E:\ruanjian\pcl\.minecraft`
- Instance: `26.2-Fabric 0.19.3`
- Instance mods directory:
  `E:\ruanjian\pcl\.minecraft\versions\26.2-Fabric 0.19.3\mods`
- Minecraft: `26.2`
- Fabric Loader: `0.19.3`
- Fabric API already installed: `0.155.2+26.2`
- Minecraft runtime requirement: Java 25

The existing Rin Fabric reference remains pinned to Minecraft 1.21.1, Fabric
Loader 0.16.14, Fabric API 0.116.14+1.21.1, and Java 21. This project must not
silently replace those verified reference pins. It will be a separate 26.2
project so the existing reference and scaffold contract remain stable.

## Delivery strategy

The feature is divided into three independently testable vertical slices.

### Phase 1: visible companion, chat, and follow

- Register a custom companion entity with a player-shaped client renderer.
- Spawn, dismiss, recall, pause, resume, and inspect it with `/companion`.
- Intercept owner messages beginning with `@伙伴` and keep them out of ordinary
  public chat.
- Create one stable Rin Session per world, owner, and companion.
- Capture bounded conversation and nearby-world observations.
- Let Rin choose only from talk, follow, stop, wait, and refuse offers.
- Generate bounded Chinese dialogue after the selected action is authorized.
- Persist identity, skin, mode, Pending Turn, Job identity, and report Outbox.

### Phase 2: survival exploration

- Add a real bounded inventory, equipment selection, food use, health recovery,
  navigation, hostile-mob defense, retreat, resource search, harvesting, and
  recipe-backed crafting.
- Support owner-following exploration and bounded solo exploration.
- Maintain one active chunk ticket around a solo companion only while an
  approved operation is running. Release it on pause, stop, death, completion,
  world unload, or timeout.
- Report partial progress for long operations and recover interrupted work as
  `cancelled` or `outcome-unknown`; never blindly replay a world mutation.

### Phase 3: autonomous building

- Convert a validated local building goal into a bounded blueprint.
- Calculate required materials from the blueprint and actual recipe graph.
- Gather, craft, transport, and place blocks incrementally.
- Save the blueprint, material ledger, cursor, and operation marker.
- Resume only after revalidating world identity, protected blocks, current
  structure state, inventory, and operation identity.

## Architecture

```text
Minecraft integrated/dedicated logical server
  ├─ CompanionEntity and player-model renderer
  ├─ chat and /companion command adapters
  ├─ bounded WorldObservation capture
  ├─ ActionCatalog: game-authored legal offers
  ├─ CompanionExecutor: server-thread Minecraft actions
  ├─ CompanionSavedState: identity, inventory workflow, markers, outbox
  └─ Rin Java SDK WorkflowCoordinator
                │ loopback HTTP
                ▼
          Rin sidecar process
                │ HTTPS
                ▼
       DeepSeek-compatible endpoint
```

The model never controls a Minecraft object or receives a generic command
executor. Rin selects from actions already authored and bounded by the mod. The
executor resolves targets and validates permissions again on the logical server
thread immediately before applying an effect.

## Project boundaries

The new project lives at `examples/mods/fabric-ai-companion`. It may reuse the
Rin Java SDK source tree in the same manner as the current Fabric reference, but
the built JAR must include the SDK classes and must not require a second SDK JAR
in the PCL instance.

The main components are:

- `AiCompanionMod`: common initialization, command registration, and lifecycle.
- `AiCompanionClient`: client renderer and skin cache registration only.
- `CompanionEntity`: physical state, movement hooks, health, and owner binding.
- `CompanionRenderer`: player model and approved skin rendering.
- `CompanionRuntime`: one runtime per logical server and server-thread dispatch.
- `CompanionSavedState`: bounded durable world and workflow state.
- `CompanionSessionState`: one Rin workflow and action state per companion.
- `CompanionWorkflowStore`: Java SDK persistence adapter.
- `CompanionRequests`: Create, Observe, Propose, Generate, and Report payloads.
- `WorldObservation`: immutable bounded data captured on the server thread.
- `ActionCatalog`: constructs only actions legal at observation time.
- `CompanionPlanner`: validates returned Proposal freshness and full offer
  identity before creating a local plan.
- `CompanionExecutor`: runs local state machines for movement, survival,
  gathering, crafting, combat, and building.
- `CompanionModelConfig`: validated Base URL and model ID, with no secret data.
- `ManagedRinSidecar`: starts and restarts the local Rin process for this PCL
  installation.

Each component has one boundary. Network futures, Minecraft entities, threads,
HTTP objects, provider responses, and credentials are never serialized into the
world save.

## Companion identity and appearance

The companion is a custom entity, not a fake authenticated player and not a
`ServerPlayerEntity`. This avoids login, permission, player-list, advancement,
and online-account ambiguity.

The client renderer uses the normal player model shape. The initial skin is the
owner's current skin. `/companion skin <player-name>` may select another Mojang
profile. Skin lookup accepts only Mojang profile/session services, caches the
signed texture property, and falls back to the owner's skin or a bundled default.
Arbitrary texture URLs are rejected.

Saved identity consists of world UUID, companion UUID, owner UUID, stable Rin
Session ID, display name, and skin profile. A missing entity can be recalled from
saved identity; a second live entity with the same companion UUID is rejected.

## Player interaction

Owner chat beginning with `@伙伴` is treated as a private companion message. The
prefix and surrounding whitespace are removed, the remaining UTF-8 text is
bounded, and an empty message is rejected. Other players cannot issue private
instructions; delegation is outside this release.

Administrative commands:

```text
/companion spawn
/companion recall
/companion pause
/companion resume
/companion status
/companion inventory
/companion skin <player-name>
/companion model show
/companion model baseurl <https-url>
/companion model name <model-id>
/companion model apply
```

Gameplay commands are owner-scoped. Model configuration commands require
permission level 4 or the integrated-server owner.

## Dynamic DeepSeek configuration

The Base URL and model name are stored in the instance configuration, not in a
world save. Defaults are:

```text
base_url = https://api.deepseek.com/v1
model = deepseek-chat
```

The API key is read only from `RIN_MODEL_API_KEY` in the PCL/game process
environment. It is never accepted in chat, commands, config files, world state,
logs, reports, or crash text.

Rin currently constructs its model provider at sidecar startup. Dynamic changes
therefore use a managed local sidecar restart instead of adding a remote provider
mutation endpoint to the framework. `ManagedRinSidecar` launches the built
`rin.exe` on loopback with the selected Base URL and model in its child
environment. Applying a new configuration performs this sequence:

1. stop accepting new companion requests;
2. retain Pending Turns and report Outbox entries;
3. request a bounded sidecar shutdown, then terminate only the owned child if it
   does not exit;
4. restart with the new Base URL and model;
5. wait for `/ready`;
6. drain reports and resume retained work.

Only `https` provider URLs are accepted, except loopback HTTP for local testing.
User information, fragments, queries, non-default embedded credentials, and
unsupported schemes are rejected. The sidecar always listens on loopback. The
mod never turns an arbitrary Base URL into a general HTTP proxy.

If the user runs an external Rin sidecar instead, managed mode can be disabled;
model changes then require restarting that external process.

## Rin session and decision flow

One stable Session ID is derived from the world UUID, owner UUID, and companion
UUID. The Actor represents the companion and contains its public traits, goals,
boundaries, activity, and memory state.

For one decision turn:

1. capture a bounded immutable observation on the server thread;
2. construct only currently legal Action Offers;
3. persist the complete Pending Turn before network work;
4. submit or resume the Proposal Job through `WorkflowCoordinator`;
5. match the Proposal to Session, Actor, Tick, Epoch, Decision Window, and the
   complete original Offer;
6. revalidate world, entity, target, inventory, recipe, protection, distance,
   and operation identity on the server thread;
7. persist an active operation marker and execute the local action state machine;
8. persist the exact Action Report in the Outbox;
9. retry reports until Rin returns a valid acknowledgement.

At most one model decision is active per companion. Server ticks advance local
actions and inspect futures; they never block on HTTP or model completion.

## Action model

The first-phase offer set is:

```text
dialogue.reply
movement.follow_owner
movement.stop
activity.wait
safety.refuse
```

Phase 2 adds bounded variants of:

```text
exploration.scout_area
survival.eat
survival.retreat
combat.defend
resource.harvest
crafting.craft_recipe
inventory.store
inventory.retrieve
navigation.return_home
```

Phase 3 adds:

```text
building.plan
building.gather_materials
building.place_next_batch
building.repair
```

Offer arguments contain stable IDs and bounded constraints authored by the mod,
not arbitrary model-supplied commands. Examples include a validated entity UUID,
registry item ID selected from an allowlist, recipe ID found by Minecraft, a
bounded radius, an approved block palette, and a blueprint ID already stored by
the host.

## Survival rules

The companion obeys normal survival constraints:

- it owns a finite inventory and equipment slots;
- it cannot place or craft an item it does not possess;
- crafting must resolve through a real Minecraft recipe;
- harvesting requires the target block, reachable position, suitable tool, and
  expected drops;
- tools lose durability through normal game operations;
- damage, hunger or equivalent food budget, death, and item drops are real;
- movement uses Minecraft navigation rather than model-generated coordinates;
- exploration and building operate within configured distance, time, chunk,
  block, and operation budgets.

Full autonomy does not grant command execution, operator permissions, creative
inventory, arbitrary NBT, teleportation, raw registry lookup from model text, or
reflection.

## World mutation policy

The companion may autonomously harvest and build, but every mutation passes a
local policy. It cannot alter bedrock, command or structure blocks, portals,
spawn-protected blocks, blocks rejected by Fabric protection callbacks, or a
container it does not own. It cannot use commands to bypass these rules.

Work is rate-limited per tick and per operation. A building operation has a
maximum volume, palette size, material count, duration, travel radius, and chunk
ticket radius. Limits are configuration values with conservative defaults and
hard upper bounds.

Because Minecraft Saved State does not make an arbitrary world mutation and Rin
Outbox update one transaction, the host initially declares `advisory`. Each
world-changing local operation therefore has a stable operation ID and a durable
cursor. Recovery re-reads the world before continuing. A block already in the
desired state counts as completed; an ambiguous mutation produces
`outcome-unknown` and pauses for reconciliation rather than repeating blindly.

## Dialogue generation

Rin Proposal selects the action. If the action needs speech, the mod separately
submits a bounded `free-response` Generation Job. The prompt contains only the
current approved action, bounded observation summary, public companion traits,
and recent bounded conversation context.

The provider must return one JSON object with a `line` string. The mod validates
object shape, UTF-8, length, control characters, and display safety. The line is
display-only and is never parsed as a command, registry ID, coordinate, recipe,
blueprint, file path, or Java name. Invalid or unavailable generation uses a
local Chinese fallback line.

## Failure and recovery behavior

- Sidecar or provider unavailable: retain work and reports; use a short local
  message and continue only safe local active operations.
- Player disconnects: stop owner-following interaction; solo work may continue
  only if explicitly active and within its budget.
- Companion unloads or dies: release chunk tickets, persist terminal state, and
  do not respawn automatically without configured recovery.
- World or timeline changes: reject stale Proposal and Generation results.
- Navigation failure: retry with a bounded local alternative, then report failure.
- Inventory full or material missing: store, return, replan, or stop; never
  delete items silently.
- Save corruption or limit violation: fail closed and preserve the unreadable
  file for diagnosis.
- Shutdown: stop accepting work, cancel network futures, persist bounded state,
  release tickets, and stop only the sidecar child owned by this mod.

## Testing

Only tests adjacent to each phase are required during implementation.

### Pure Java tests

- model config URL and model validation;
- chat prefix parsing and owner authorization;
- state encode/decode, limits, corruption, and migration;
- offer identity and Proposal binding;
- inventory, recipe, blueprint, and operation-cursor state machines;
- restart behavior for Pending Turn and Outbox.

### Fabric GameTests

- companion spawn and owner binding;
- server-thread dispatch;
- player-model entity persistence across server restart;
- follow, stop, navigation failure, and stale Epoch rejection;
- harvest, craft, inventory, death, and chunk-ticket cleanup;
- bounded building placement and interrupted-build recovery.

### Local PCL2 acceptance

- build a Java 25/Fabric 0.19.3/Minecraft 26.2 JAR;
- copy it to the verified instance `mods` directory without removing existing
  Fabric API;
- start Rin and PCL2, create a disposable test world, and verify chat, follow,
  save/reload, provider restart, solo exploration, gathering, crafting, and a
  small bounded build;
- inspect the game log and Rin diagnostics for uncaught errors, duplicate action
  application, retained reports, and leaked credentials.

Testing uses a disposable world first. Existing user worlds are not modified
until the mod passes the restart and bounded-mutation gates.

## Acceptance criteria

The complete feature is accepted when:

1. the mod loads in the verified PCL2 Fabric 26.2 instance;
2. the player can spawn one player-shaped companion and change its approved
   skin;
3. `@伙伴` messages receive model-backed Chinese replies without blocking ticks;
4. the companion follows and explores with the player;
5. the companion can perform bounded solo exploration while preserving state;
6. it gathers real drops, uses real recipes, consumes resources, and manages a
   finite inventory;
7. it can plan and complete a small survival build, survive interruption, and
   resume without blindly duplicating mutations;
8. Base URL and model can be changed by an administrator without exposing the
   API key;
9. provider outages, restarts, player disconnects, and world reloads fail closed
   without freezing Minecraft or granting a second action;
10. no model text becomes a command, arbitrary URL request, registry identifier,
    coordinate, reflection target, or unvalidated world mutation.

## Implementation order

The next implementation plan covers Phase 1 only, including the separate 26.2
Fabric project, Java 25 toolchain, player-shaped entity, private chat, dynamic
model configuration, managed local sidecar, Rin workflow persistence, dialogue,
follow, stop, wait, refusal, Gradle build, installation, and PCL2 smoke
preparation.

Phase 2 and Phase 3 each receive their own plan and verification gate after the
preceding phase works in the disposable PCL2 world. This prevents exploration,
crafting, chunk loading, and building failures from being debugged simultaneously.

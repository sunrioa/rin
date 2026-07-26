# Game adapters

[English] | [简体中文](game-adapters.zh-CN.md)

Adapters translate engine lifecycle events into Rin `0.7.0` **Preview**
protocol objects. They do not give Rin authority over the game.

## Minimal host loop

1. Restore a stable Session identity, Pending Turn, applied-operation markers,
   and report outbox before network work.
2. Capture one authoritative Observation, Epoch, host Timepoint, and monotonic
   Observation Sequence.
3. Build a Decision Window and 1–32 fully bound Action Offers.
4. Persist the complete Pending Turn, then submit or resume the proposal.
5. Match the returned Proposal to the durable Session, Request, Actor, Window,
   and selected Offer.
6. Recheck Epoch, deadline, capability digest, targets, and game rules on the
   authority thread.
7. Execute with a stable operation ID or reject.
8. Persist the exact `ReportActionRequest` in an Outcome Outbox and retry until
   acknowledged.

Never turn a transport error or ambiguous Job result into permission to perform
a second action.

## Threading

- Rendering, input, and simulation threads must not block on HTTP or model
  completion.
- Capture engine objects on their owning thread, but persist only plain bounded
  data and opaque `HostRef` values.
- Resolve `HostRef` and execute capabilities only on the authority thread.
- Threads, Tasks, Futures, sockets, HTTP objects, and tokens never enter a game
  save.

## Persistence

The reusable SDK coordinator owns workflow ordering; the game owns storage
guarantees. Select an honest [Host durability profile](host-durability.md).
A standalone JSON file normally proves only `advisory`. Claim
`idempotent-action` only when game state makes `operation_id` retry-safe, and
`transactional-action` only when the effect, marker, and outbox entry share one
game transaction.

State readers must distinguish:

- `not_found`: initializing a new identity is allowed;
- valid state: resume it;
- unreadable, malformed, oversized, or inconsistent state: fail closed.

Bound Pending Turns, markers, outbox entries, and state-file bytes. Flush and
replace files portably; test interrupted replacement on Linux and Windows.

## Ren'Py

Copy [`rin_client.py`](../adapters/renpy/rin_client.py),
[`rin_epoch.py`](../adapters/renpy/rin_epoch.py), and
[`rin_bridge.rpy`](../adapters/renpy/rin_bridge.rpy). Use the client from a
background worker and return plain dictionaries to the main thread. Call
`rin_bind_host_epoch` with game-supplied stable save and world IDs before
creating offers.

The bridge stores only `rin_host_epoch_state` in the rollback/save store. A
bounded, plain-data `persistent.rin_host_epoch_ledger` keeps Host and Timeline
high-water marks outside rollback and merges replicas by maximum generation.
`after_load` and the first interaction in each rollback sequence fork the
Timeline and invalidate every registered result, including completed but
unconsumed work. Scheduling and resuming fail closed unless the decision window
and every offer match the current Epoch exactly; late worker results become
`stale_epoch`. The bridge calls `renpy.block_rollback()` after load because the
official Ren'Py guidance requires callback migrations not to be rolled back.

The worker registry remains process-local; persistent story state must contain
the complete Pending Turn and any returned Job ID. Never put a worker, lock,
HTTP response, or token in rollback/save data.

The Python suite covers process restart, old-save load, repeated rollback,
persistent-ledger merge, bounds, malformed state, worker invalidation, and late
completion. These logic tests run on macOS, Linux, and Windows. Ren'Py 8.5.3
lint plus an engine-run rollback harness pass locally. A real project must
still verify visible save/load, screen updates, shutdown, and Sidecar restart
in its exact Ren'Py build.

## Godot 4

The [Godot reference](../examples/godot/README.md) uses `HTTPRequest` signals
and a per-slot `RinWorkflow`. It stores bounded JSON under
`user://rin/<slot>.json`, saves Pending Turn and outbox state before network
work, and never performs world mutation inside the transport client.

Call the workflow from gameplay events or authoritative simulation steps, not
`_process()` every frame. Replace its advisory file store or make execution
idempotent before using it for durable world mutation.

## Unity

The [Unity UPM package](../examples/unity/README.md) contains:

- `RinClient`: bounded, no-redirect `UnityWebRequest` coroutine transport;
- `RinUnityWorkflow`: startup recovery, Epoch management, Pending Turn,
  operation marker, and report outbox;
- `IRinUnityHost`: game-owned `CaptureTurn` and idempotent `Execute` boundary.

`ActionOffer.arguments` remains an arbitrary JSON object without forcing a
dialogue- or engine-specific DTO. The harness compiles the package and verifies
restart, backup recovery, corrupt-state failure, disk-write failure, and raw
argument preservation on Linux and Windows. This does not replace a licensed
Unity Editor/Player test.

## Unreal

The Preview [Unreal Runtime Plugin](../examples/unreal/RinHost/README.md) uses a
`UGameInstanceSubsystem` and an owning-Game-Instance world delegate. The game
must inject stable Session, Host, World, and Timeline generations from its
authoritative save; the adapter does not guess them from PIE or map names.
Capability registration and `AuthorizeAndQueueInvocation` execute on the Game
Thread, and a Behavior Tree MoveTo task demonstrates monotonic long-action
reporting. World replacement and timeline forks convert unfinished work to
`outcome-unknown`.

Linux and Windows CI statically reject unsafe execution surfaces and
case-insensitive or reserved paths. This is not an Unreal Header Tool, compiler,
Editor, packaged Player, SaveGame transaction, or navigation runtime test.

## Mod hosts

Fabric, BepInEx Mono/IL2CPP, and Luanti examples demonstrate server/main-thread
dispatch plus durable SDK workflow stores. They are advisory references, not
proof that a generated NPC is safe for every game's save or threading model.
Use [`rin init host`](host-scaffolding.md) to generate a pinned starting project,
then complete the [real-host validation matrix](host-integration-validation.md).

## Engine-independent review

- Offers contain only actions already legal at capture time.
- High-authority effects remain in game code.
- Pathfinding uses engine navigation/physics APIs when available; vision
  models are an optional observation source, not the default movement system.
- TTS consumes approved dialogue after the text decision; audio playback never
  changes action authority.
- Long operations report queued/running/terminal progress and use cooperative
  cancellation where the engine supports it.
- Storage metrics, retention, snapshot/export, and logs are configured for the
  expected lifetime of the Session.

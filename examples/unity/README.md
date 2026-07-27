# Rin Unity adapter

[English](README.md) | [简体中文](README.zh-CN.md)

This directory is an installable Unity Package Manager package that declares
Unity `2021.3` as its minimum API level. Add it from a local checkout, attach `RinClient` and
`RinUnityWorkflow` to one persistent GameObject, assign the client field, then
keep the small `RinNpcExample` in game-owned code.

`RinUnityWorkflow` stores at most 1 MiB under
`Application.persistentDataPath/rin/default.json`. It preserves the stable
playthrough and trusted content binding, complete Pending Turn, opaque authored
arguments, an Active Run marker, tick high-water, applied markers, and up to 64
exact reports. A flushed temporary file and recoverable target/backup replace
sequence work on Windows. State schema 3 intentionally rejects the earlier
lossy preview format instead of pretending its `{}` argument placeholders can
be recovered.

Every managed-domain lifetime advances Host and Timeline generations. A scene
load advances World generation. `RinUnityActionGate` cancels an active action
before authority replacement, reports `outcome-unknown` when cancellation
cannot be proved, dispatches background completion back to the Unity thread,
and ignores late callbacks from an older generation. A domain reload recovers
a persisted Active Run as `outcome-unknown`; it never executes that operation
again blindly.

Local validation uses the protocol ceilings: identifiers are at most 96 safe
characters and all wire counters are at most `9007199254740991`.

`RinNpcExample` demonstrates a game-authored `movement.move_to` offer.
`RinNavMeshAction` owns `NavMeshAgent.SetDestination`, observes completion over
later frames, resets the path on cancellation, and returns a typed terminal
result. The model chooses the sealed offer; it does not supply a transform,
arbitrary coordinates, a method name, or a console command. Replace the
example destination and capability allowlist with game-owned values.

The durability profile remains `advisory`: movement and this sidecar file do
not share a transaction. Production games should make the operation ID
idempotent in their authoritative save or replace this boundary with their save
transaction.

The token is read at runtime from `RIN_TOKEN`; it is never serialized into a
scene or prefab. The default loopback HTTP endpoint needs no token.

`tools/verify_unity.py` compiles the package against strict Unity API stubs and
runs binding, Scene/Domain generation, cancellation, late-callback, opaque
argument, Active Run recovery, and file-replacement tests on .NET 6. CI runs it
on Linux and Windows.
This is a package/compiler verification, not a Unity Editor import or built
Player test. Run the
[real-host validation matrix](../../docs/host-integration-validation.md) before
claiming compatibility with a particular Unity release or scripting backend.

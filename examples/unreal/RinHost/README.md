# Rin Unreal Runtime Plugin skeleton

[English](README.md) | [简体中文](README.zh-CN.md)

This Preview Runtime Plugin maps Rin's universal Host boundaries to Unreal
without moving game authority into the sidecar.

- `URinHostSubsystem` follows `UGameInstanceSubsystem` lifetime.
- `FWorldDelegates::OnPostWorldInitialization` advances World Epoch only for
  the owning Game Instance.
- capability registration and final invocation authorization run on the Game
  Thread; `DispatchToGameThread` is the only cross-thread entry.
- Blueprint sees typed capability, Epoch, invocation, and ActionRun values. It
  never receives a shell, console-command, reflection, or arbitrary function
  execution surface.
- `UBTTask_RinHostMoveTo` demonstrates mapping one long-running semantic action
  to native Behavior Tree movement and monotonic ActionRun callbacks.

At load, the game must call `ConfigureHostIdentity` with a stable save/profile
ID and a persisted boot generation, then `BindWorldIdentity` with its
authoritative world and timeline generations. The plugin deliberately does not
derive identity from a process GUID, object pointer, PIE name, or map path.
The local boundary limits identifiers to 96 safe characters and Epoch/progress
counters to `1..9007199254740991`; increments fail closed at the ceiling.
Before publishing a Decision Window, the game calls `ObserveAuthoritativeClock`
and then `ReplaceActionOffers` with the complete current Host-authored offer
set. `OfferDigest` must be the SHA-256 of the canonical complete offer,
including opaque arguments and targets. A selected invocation repeats the
offer identity, actor, capability, Epoch, observation sequence, deadline, and
digest exactly; only `OperationId` is additional.

`AuthorizeAndQueueInvocation` consumes one current offer and performs the final
Epoch, complete offer binding, authoritative deadline, exact capability
version/digest, revocation, and duplicate-operation checks in one Game Thread
call. Capability revocation or deadline advancement marks matching queued runs
`stale`, and `ReportRun(... Running ...)` repeats both checks immediately before
Behavior Tree execution. Authorize before enqueuing `UBTTask_RinHostMoveTo`;
the task reports `running` and a terminal result for that queued operation.

World replacement invalidates the bound World Epoch and changes unfinished
runs to `outcome-unknown`. Rebind only after the authoritative save has loaded.
`ForkTimeline` similarly invalidates unfinished work before advancing the
timeline generation. Every progress callback carries the Epoch captured at
start, so a callback from an unloaded World cannot revive the old run.

Copy `RinHost/` under a project's `Plugins/` directory, regenerate project
files, and build with that project's installed Unreal Engine. The repository
performs static structure/security checks on Windows and Linux; it does not
claim Unreal Editor or packaged-Player validation.

The bounded in-memory operation set and run map are deliberately not advertised
as durable. A real game must connect them, Pending Decision, and exact Outbox
records to its authoritative SaveGame/database transaction. The effect and
operation marker must commit together before claiming `idempotent-action`; the
effect, marker, and outbox must commit together before claiming
`transactional-action`.

The lifecycle choice follows Epic's
[Programming Subsystems](https://dev.epicgames.com/documentation/en-us/unreal-engine/programming-subsystems-in-unreal-engine)
and [Unreal Engine Modules](https://dev.epicgames.com/documentation/en-us/unreal-engine/unreal-engine-modules)
guidance. World initialization uses the documented
[`FWorldDelegates`](https://dev.epicgames.com/documentation/en-us/unreal-engine/API/Runtime/Engine/FWorldDelegates)
callback.

# Universal Host SDK

[English](host-sdk.md) | [简体中文](host-sdk.zh-CN.md)

`sdk/hostkit` is the executable Go reference for the boundary between Rin and
an authoritative game. It coordinates protocol DTOs; it does not implement an
engine, navigation, physics, save system, model provider, or arbitrary command
runner.

## Ports

| Port | Owner and purpose |
| --- | --- |
| `RinTransport` | Submit/poll Proposal Jobs and report exact action lifecycle records. |
| `AuthorityDispatcher` | Marshal final authorization and execution onto the game-owned thread. |
| `HostStateStore` | Persist revisioned Pending Decision, ActionRun, and Outbox state. |
| `IdentityProvider` | Read stable Session/Epoch/time identity and issue non-content-derived IDs. |
| `ObservationMapper` | Convert bounded engine events to immutable observation DTOs. |
| `CapabilityRegistry` | Resolve, bind, and repeat TOCTOU authorization for exact capabilities. |
| `ActionExecutor` | Execute or cancel one already-authorized invocation in the game. |
| `ArtifactPresenter` | Present immutable external artifacts without granting world authority. |

Engine objects, threads, sockets, futures, provider tokens, model output, and
unbounded binary data must not enter `WorkflowState`.

## Coordinator lifecycle

1. `BeginDecision` validates the game-authored request and commits the Pending
   Decision before any network call. It refuses a second Pending Decision or a
   nonempty Outcome Outbox.
2. `ResumePendingWork` drains exact reports first. It submits with the retained
   request identity only when no Job ID exists, saves that Job ID, and performs
   one bounded poll without an internal wait loop. A crash between submit and save is recovered through
   Rin's idempotent request identity.
3. `DispatchAndEnqueue` verifies that the Proposal selected an exact offer from
   the Pending Decision. It binds the current descriptor digest and Epoch,
   repeats authorization on the authority thread, executes through
   `ActionExecutor`, and commits the exact accepted Action Report to the Outbox.
4. `RecordTransitionAndEnqueue` accepts only monotonic queued/running/terminal
   transitions. A successful outcome after its invocation deadline is rejected.
5. `ReconcileEpoch` drops an unsubmitted stale decision and cancels stale active
   actions. If a capability cannot be cancelled or was dynamically removed, the
   result becomes `outcome-unknown`; the framework never invents successful
   rollback.
6. `DrainOutbox` removes an entry only after Rin acknowledges its exact
   `ReportActionRequest`. Transport failure retains equivalent DTO content and
   stable IDs for retry.

`HostStateStore.CommitEffect` is the durability boundary. A
`transactional-action` Host must publish its game effect and returned
`WorkflowState` atomically. An `idempotent-action` Host may apply by stable
Operation ID before saving. An `advisory` Host may be best effort but must not
claim either stronger profile.

## Long-running movement

Movement is a capability implemented by the game, not a universal coordinate
command. A Unity Host may map it to NavMesh, an Unreal Host to a Gameplay
Ability or Behavior Tree task, and a server Mod to its native pathfinder.
`ActionExecutor.Execute` returns `queued` or `running`; engine callbacks later
call `RecordTransitionAndEnqueue`. Scene/world/timeline changes call
`ReconcileEpoch`, which prevents a late callback from mutating the replacement
world.

## Cross-language status

The Go package is the normative, type-checked port and state-machine reference.
JavaScript, C#, Java, Lua, Godot, Unity, Fabric, BepInEx, and Luanti already
ship the protocol-v2 Pending Turn and exact-Outbox workflow. Their engine-facing
interfaces should use these same eight boundaries; language-specific syntax
does not change ownership, Epoch, retry, or ActionRun semantics.

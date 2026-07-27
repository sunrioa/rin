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
| `IdentityProvider` | Read stable Session/Epoch/time/Principal identity and issue restart-safe, never-reused IDs. |
| `ObservationMapper` | Convert bounded engine events to immutable observation DTOs. |
| `CapabilityRegistry` | Resolve, bind, and repeat TOCTOU authorization for exact capabilities. |
| `ActionExecutor` | Execute or cancel one already-authorized invocation in the game. |
| `ArtifactPresenter` | Present immutable external artifacts without granting world authority. |

Engine objects, threads, sockets, futures, provider tokens, model output, and
unbounded binary data must not enter `WorkflowState`.

## Coordinator lifecycle

1. `BeginDecision` validates the game-authored request, obtains the Operation ID
   only from `IdentityProvider`, and commits the Pending Decision before any
   network call. It refuses a second Pending Decision, a nonempty Outcome
   Outbox, or exhausted Action/Outbox capacity.
2. `ResumePendingWork` drains exact reports first. It submits with the retained
   request identity only when no Job ID exists, saves that Job ID, and performs
   one bounded poll without an internal wait loop. A crash between submit and
   save is recovered through Rin's idempotent request identity. A transport
   must wrap `ErrProposalJobNotFound` for wire error `job_not_found`; HostKit
   then durably clears the stale Job ID and exact-resubmits the retained request
   after a Sidecar restart.
3. `DispatchAndEnqueue` verifies that the Proposal selected an exact offer from
   the Pending Decision. Before any game effect, it preflights retained-state
   capacity and report metadata. On the authority thread it binds the current
   descriptor digest and Epoch, checks the trusted Principal's granted scopes,
   executes through `ActionExecutor`, validates structured Output against the
   sealed capability Output Schema, and commits the exact accepted Action
   Report to the Outbox.
4. `RecordTransitionAndEnqueue` accepts only monotonic queued/running/terminal
   transitions. A successful outcome after its invocation deadline is rejected.
5. `ReconcileEpoch` drops an unsubmitted stale decision and cancels stale active
   actions. If a capability cannot be cancelled or was dynamically removed, the
   result becomes `outcome-unknown`; the framework never invents successful
   rollback.
6. `DrainOutbox` removes an entry only after Rin acknowledges its exact
   `ReportActionRequest`. Transport failure retains equivalent DTO content and
   stable IDs for retry. Once the last report for a terminal action is
   acknowledged, HostKit removes that full Action record.

`WorkflowState` version 2 retains only active actions and terminal actions with
unacknowledged reports. Both Actions and Outbox are capped at 1024 entries.
Ten thousand acknowledged immediate actions therefore leave no growing action
ledger. Idempotent/transactional Hosts keep their applied-operation marker in
the authoritative game store; HostKit does not duplicate that marker in an
unbounded summary. `IdentityProvider.NewID` must never reuse an ID in the same
Session, including across process restart.

If execution has started but the executor returns an error, malformed
lifecycle data, or schema-invalid Output, HostKit commits a recoverable
`outcome-unknown` Action and exact Outbox report before returning
`ErrExecutionOutcomeUnknown`. It does not retry the world effect.

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
The dependency-free [C99 reference](../examples/native-host) applies the same
Epoch, descriptor, Operation ID, and ActionRun rules on GCC/Clang and MSVC.
JavaScript, C#, Java, Lua, Godot, Unity, Fabric, BepInEx, and Luanti already
ship the protocol-v2 Pending Turn and exact-Outbox workflow. Their engine-facing
interfaces should use these same eight boundaries; language-specific syntax
does not change ownership, Epoch, retry, or ActionRun semantics.

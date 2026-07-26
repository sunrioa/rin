# Rin examples

[English](README.md) | [简体中文](README.zh-CN.md)

Start with [`basic`](basic/). It is intentionally small and demonstrates only
creating a Session and recording one observation against a running Sidecar.
Its process-local IDs make it a development smoke test, not a production save
architecture.

Use [`recovery`](recovery/) when designing a real integration. It demonstrates
stable identities, durable Proposal Attempts, applied-operation markers, an
authoritative Outcome Outbox, exact retry, offline reconciliation, Snapshot
binding, durable temporary-file replacement, and restart recovery. The extra size is
isolated here so the quickstart remains readable.

Use the installable Node.js 18+ [`terminal-story`](terminal-story/) for the
Windows/macOS/Linux playable vertical slice, safe JavaScript SDK workflow,
reproducible Sidecar benchmark, and honest persistent-rule-tree comparison.

The engine and mod directories demonstrate host-specific threading and
packaging. They persist stable workflow recovery state, but remain
`advisory`: a real integration must connect effect application and operation
markers to the game's authoritative save/idempotency boundary. See the
[real-host validation matrix](../docs/host-integration-validation.md) before
claiming production stability.

[`native-host`](native-host) is a dependency-free C99 reference for native
engines. It runs the shared Host scenarios on GCC/Clang and MSVC without
introducing an engine, JSON, HTTP, or shell dependency.

[`unreal/RinHost`](unreal/RinHost) is a Preview Unreal Runtime Plugin skeleton
for explicit save/world Epoch binding, Game Thread authorization, typed
Blueprint capabilities, and Behavior Tree ActionRun reporting. CI checks its
portable layout and safety boundary; an Unreal Editor build remains a manual
gate.

[`unity`](unity) is an installable UPM reference with Domain/Scene Epochs,
durable Active Run recovery, cancellable long-action handles, and a
game-authored NavMesh movement example. Strict API stubs cover its portable
contract; Editor and Player builds remain manual gates.

[`godot`](godot) is a runnable Godot 4.6.3 project with durable
Host/World/Timeline generations, exact Decision Window/Offer binding, and
Active Run recovery. Official headless binaries exercise the lifecycle on
Linux and Windows; Editor and exported-build traffic remain manual gates.

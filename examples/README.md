# Rin examples

[English](README.md) | [简体中文](README.zh-CN.md)

Start with [`basic`](basic/). It is intentionally small and demonstrates only
creating a Session and recording one observation against a running Sidecar.
Its process-local IDs make it a development smoke test, not a production save
architecture.

Use the dependency-free Go [`terminal-story`](terminal-story/) for a runnable
V2 dialogue and story-progression slice. Its integration tests prove that an
internal Agent Runtime and an external MCP client share the same policy and
authoritative operation path.

[`adapters/grid`](adapters/grid/) is the engine-neutral V2 reference Adapter.
Its tests drive observation, binding, effect policy, resource collection,
container transfer, cancellation, restart rejection, and authoritative
outcomes through the same HostKit and Control Plane used by real games:

```sh
go test ./examples/adapters/grid
```

[`adapters/story`](adapters/story/) applies the same contract to dialogue,
relationship changes, story progress, and an enforceable character boundary:

```sh
go test ./examples/adapters/story
```

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

[`mods/luanti-rin-npc`](mods/luanti-rin-npc) is a complete loopback-only
Luanti server Mod. Official Luanti 5.16.1 dedicated servers load both the
source Mod and a newly generated scaffold twice against the same world;
multiplayer, live Sidecar, forced-stop, and soak behavior remain manual gates.

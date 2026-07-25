# BepInEx Rin NPC reference

[English](README.md) | [简体中文](README.zh-CN.md)

This is a buildable BepInEx 6 reference, split by Unity backend.

**Host capability profile: `advisory`.** The example persists stable identity,
Pending Turn, Job ID, and Outcome Outbox state, but a generic BepInEx plugin
cannot atomically combine an arbitrary game's save mutation with that file.
See [Host capability profiles](../../../docs/host-capability-profiles.md).

## Choose exactly one backend

| Backend | Project | Target | What is included |
| --- | --- | --- | --- |
| Unity Mono | `RinNpc.Mono` | `netstandard2.0` | Loadable plugin, F8 demo, Unity main-thread queue |
| Unity IL2CPP | `RinNpc.IL2CPP` | `net6.0` | Loadable transport plugin; game-specific hook required |

Both projects pin BepInEx `6.0.0-be.785`. Do not install both variants. IL2CPP
interop assemblies are generated for a particular game, so this repository
does not pretend a generic `UnityEngine` reference is sufficient. A real
IL2CPP adapter must set `Plugin.ApplyDialogue` to a delegate that marshals onto
the game's owning thread, then call `RequestNpcTurnAsync` from its interaction
hook.

## Build and install

With the .NET 6 SDK:

```bash
dotnet restore RinNpc.Mono/RinNpc.Mono.csproj --locked-mode
dotnet build RinNpc.Mono/RinNpc.Mono.csproj -c Release --no-restore

dotnet restore RinNpc.IL2CPP/RinNpc.IL2CPP.csproj --locked-mode
dotnet build RinNpc.IL2CPP/RinNpc.IL2CPP.csproj -c Release --no-restore
```

From the repository root, build deterministic install ZIPs on Linux, macOS,
or Windows:

```bash
python tools/package_bepinex.py
```

Extract the ZIP for the correct backend into the game root. It creates
`BepInEx/plugins/RinNpc`. The Mono bundle includes the `System.Text.Json`
runtime dependencies needed by older Unity Mono installations; the IL2CPP
bundle relies on its .NET 6 runtime. Neither bundle copies BepInEx, Unity, nor
game-specific interop assemblies.

Start the game once, then configure:

- `Connection.BaseUrl`: defaults to loopback Rin.
- `Identity.ProductIdentity`: stable per game; never use an executable path.
- `Identity.SaveIdentity`: stable per save/profile. Replace the demo value
  before production use.
- `Example.EnableF8Demo`: Mono-only isolated demonstration.

Pass a remote Rin bearer token only through `RIN_TOKEN`; do not put it in the
BepInEx config or game save. Remote origins require HTTPS.

## Recovery and authority

`RinNpc.Core` owns Create/Observe/Proposal Job recovery, freshness checks,
allowlisting, outcome construction, and Outbox drain. The backend wrappers only
own lifecycle, configuration, tick capture, logging, and main-thread apply.
At this revision the Mono wrapper is 106 lines and the IL2CPP wrapper is 88;
the reusable runtime is 228 lines. The larger state-store file is persistence
infrastructure rather than workflow code copied into each game.
The state file is bounded to 2 MB and the Outbox to 32 entries. Its name is a
SHA-256-derived Windows-safe filename under
`BepInEx/config/rin-npc-example`.

The F8 vertical slice is intentionally small but no longer dialogue-only. Rin
may offer an authored beacon quest and later advance it; the game Store owns
the `0 -> 1 -> 2` transition and persists the operation ID before settlement,
so a crash/retry cannot advance it twice. The current quest stage is included
in the next Observation, making the durable effect visible to later memory and
planning. Invalid-stage, stale, or non-allowlisted actions are rejected, while
the authored offline fallback remains `wait`.

The complete Pending Turn is persisted before network submission and its Job
ID immediately after `202`. A restart resumes the same operation and stable
Session. Applying an action stores the exact Commit plus a safe absolute-fact
Observe fallback. Temporary failures retain the Commit; only explicit terminal
errors such as `unknown_proposal` persist the conversion before sending
Observe.

File replacement is crash-resistant ordering, not a claim of a durable game
transaction. A process or power failure can still occur between the game
effect and state-file replacement. A production adapter should either make the
effect idempotent by operation ID or implement a game-specific transactional
store before declaring a stronger profile. Do not run two plugin instances
against the same state file.

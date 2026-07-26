# BepInEx Rin NPC reference

[English](README.md) | [简体中文](README.zh-CN.md)

This is a buildable reference for the upstream bleeding-edge, unreleased
BepInEx 6 line, split by Unity backend.

**Host durability profile: `advisory`.** The example persists stable identity,
Pending Turn, Job ID, and Outcome Outbox state, but a generic BepInEx plugin
cannot atomically combine an arbitrary game's save mutation with that file.
See [Host durability profiles](../../../docs/host-durability.md).

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
hook. Successful compilation is not proof that either backend loads in a
particular game; complete the
[real-host validation matrix](../../../docs/mod-integration-validation.md).

## Build and install

With the .NET 6 SDK:

```bash
dotnet restore RinNpc.Mono/RinNpc.Mono.csproj --locked-mode
dotnet build RinNpc.Mono/RinNpc.Mono.csproj -c Release --no-restore

dotnet restore RinNpc.IL2CPP/RinNpc.IL2CPP.csproj --locked-mode
dotnet build RinNpc.IL2CPP/RinNpc.IL2CPP.csproj -c Release --no-restore
```

From this directory, build and independently verify deterministic install ZIPs
on Linux, macOS, or Windows:

```bash
python package_bepinex.py
python package_bepinex.py --verify-archive dist/rin-npc-bepinex-mono-0.7.0.zip
python package_bepinex.py --verify-archive dist/rin-npc-bepinex-il2cpp-0.7.0.zip
```

The repository-root `python tools/package_bepinex.py` command delegates to this
same canonical helper.

Extract the ZIP for the correct backend into the game root. It creates
`BepInEx/plugins/RinNpc`. The Mono bundle includes the `System.Text.Json`
runtime dependencies needed by older Unity Mono installations; the IL2CPP
bundle relies on its .NET 6 runtime. Each bundle rejects every DLL outside its
reviewed allowlist, including BepInEx, Unity, and game-specific interop
assemblies. Review runtime files and redistribution notices before extending
an allowlist. Both include `LICENSE-RIN.txt`; the Mono ZIP
also includes the reviewed, content-pinned .NET license and notices from
`third-party/`, while the IL2CPP ZIP deliberately omits this Mono-only set. Their
`manifest.json` records every install file's SHA-256 checksum.

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
At this revision the Mono wrapper is 107 lines and the IL2CPP wrapper is 89;
the reusable runtime is 247 lines. The larger state-store file is persistence
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
an unresolved Proposal fails closed without inventing an offline action.

The complete Pending Turn is persisted before network submission and its Job
ID immediately after `202`. A restart resumes the same operation and stable
Session. Applying an action stores the exact Action Report. Every reporting
error retains that exact entry for retry; it is never converted into an
Observation.

File replacement is crash-resistant ordering, not a claim of a durable game
transaction. A process or power failure can still occur between the game
effect and state-file replacement. A production adapter should either make the
effect idempotent by operation ID or implement a game-specific transactional
store before declaring a stronger profile. Do not run two plugin instances
against the same state file.

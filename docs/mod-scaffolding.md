# Mod scaffolding

[English](mod-scaffolding.md) | [简体中文](mod-scaffolding.zh-CN.md)

`rin init mod` creates a self-contained starting project for one supported game
host. It removes mechanical SDK vendoring and manifest wiring; it does not
guess game-specific save, thread, or world-mutation APIs. Rin `0.7.0` and the
generated projects are Preview software, and every generated integration starts
with the `advisory` host durability profile.

Generation, compilation, and real-game stability are separate evidence levels.
A successful command proves that its inputs and embedded template passed local
validation. A successful build proves that the generated source compiles
against the pinned host dependencies. Neither result proves that the Mod is
stable in a particular game; use the
[real-host validation matrix](mod-integration-validation.md) before making that
claim.

## Command contract

List the templates embedded in the installed Rin binary:

```bash
rin init mod --list-hosts
```

Generate a project:

```text
rin init mod \
  --host fabric|bepinex-mono|bepinex-il2cpp|luanti \
  --id <mod_id> \
  [--name <display name>] \
  [--namespace io.github.user] \
  [--author <author>] \
  [--version 0.1.0] \
  [--output relative/path] \
  [--dry-run]
```

`--host` and `--id` are required. `--name` defaults to the Mod ID,
`--version` defaults to `0.1.0`, and `--output` defaults to `./<mod_id>`.
`--namespace` is required for Fabric and both BepInEx backends; Luanti rejects
it because this template has no global owner-namespace field. For Fabric it is
the Java package prefix; for BepInEx it is the globally unique plugin-GUID
prefix, while the generated C# namespace comes from `--id`. `--author` is
optional and is never inferred from the operating system, Git configuration,
or repository metadata. For Luanti, a supplied `--author` is written to
`mod.conf` and therefore must be a ContentDB username: 1–64 ASCII letters,
digits, underscores, or hyphens.

`--dry-run` performs the same validation and rendering, prints the deterministic
file and SHA-256 inventory, and writes nothing. It is the recommended first
command when integrating into an existing game repository.

The four exact host IDs are `fabric`, `bepinex-mono`, `bepinex-il2cpp`, and
`luanti`; names are case-sensitive and no broad `bepinex` alias is accepted.

### Identifier rules

- `--id` must be 2–64 characters and match
  `[a-z][a-z0-9]*(?:_[a-z0-9]+)*`. The deliberately narrow grammar is portable
  across the supported templates. It is stricter than Fabric's accepted
  grammar, which also permits hyphens.
- `--name` is the player-facing display name. It must be valid UTF-8, nonempty,
  and free of NUL, newlines, and control characters.
- `--namespace` is a lowercase, dot-separated reverse-domain owner namespace
  such as `io.github.example`. Empty segments, language keywords, path
  separators, and Windows device-name segments are rejected.
- `--version` must use the numeric `major.minor.patch` form, contain at most
  17 ASCII characters, and keep every component between `0` and `65534`.
  It is the generated Mod's version, not the Rin version.
- `--output` is a relative path below the current directory. Absolute paths,
  `.`/`..` traversal, alternate Windows separators, drive or UNC paths, and
  output ancestors below the current directory that are symbolic links are
  rejected. Its parent directory must already exist.

These restrictions apply on every operating system, not only on Windows.
Every path component is checked case-insensitively and against Windows device
names such as `CON`, `PRN`, `AUX`, `NUL`, `COM1` through `COM9`, and `LPT1`
through `LPT9`. Components with Windows-reserved characters, trailing spaces,
or trailing periods are also rejected. This keeps a project generated on
Linux or macOS movable to Windows without renaming files. The generator also
requires the deepest generated absolute path to fit a portable budget of 240
UTF-16 code units. On Windows, generate under a short ASCII path outside
OneDrive or other synchronizing folders; for example, use `C:\src` rather than
a deeply nested Desktop or Documents path.

## Safe and deterministic output

The generator is offline: it reads only templates and SDK sources embedded in
the exact `rin` binary being run. It does not download a newer template, inspect
an unrelated Git checkout, read credentials, or execute host build tools.
Given the same Rin binary and arguments, generated relative paths, UTF-8 file
bytes, and the sorted SHA-256 manifest are identical regardless of time,
current username, or destination directory.

Each project includes the complete source-first SDK required by its host and a
hash manifest that records every generated file except the manifest itself,
its origin Rin release, and its SHA-256 digest. Generated builds must not
depend on paths such as
`../../../sdk`; the project remains buildable after it is moved outside the Rin
repository. The vendored SDK retains Rin's MIT license notice. The generator
does not select a license for the game author's own Mod. Fabric scaffolds also
carry the exact Gradle 8.14.3 `LICENSE` and `NOTICE` as
`LICENSE-GRADLE.txt` and `NOTICE-GRADLE.txt` for the redistributed Wrapper.
BepInEx Mono scaffolds carry a reviewed, hash-pinned license/notice set only
for the eight .NET runtime DLLs their install ZIP redistributes. IL2CPP
scaffolds neither redistribute those DLLs nor claim their Mono-only notices.

The destination must not already exist, even when it is an empty directory or
a symbolic link. There is intentionally no overwrite or force mode. The
generator also rejects case-insensitive sibling collisions and symbolic links
in the destination ancestry below the current directory. If generation fails
after creating the target, it leaves the partial tree in place and normally retains
`.rin-scaffold.incomplete`; do not build or install that tree. Inspect it and
delete or move it manually before retrying. The generator never performs
automatic path-based cleanup, so it cannot delete a directory or file that a
concurrent process replaced. Generate into a new sibling directory and review
the diff when upgrading a scaffold.

Generation itself requires no network. The first Fabric or BepInEx build may
contact the pinned Gradle, Maven, or NuGet sources to obtain dependencies.
Wrapper distributions, dependency versions, and lock files remain fixed by
the template; do not replace them with floating versions merely to make a
restore succeed.

## Quick starts

The examples below use generic public identifiers. Run them from the directory
that is allowed to contain the new project.

### Fabric

```bash
rin init mod \
  --host fabric \
  --id guide_npc \
  --name "Guide NPC" \
  --namespace io.github.example \
  --author example \
  --output guide_npc

cd guide_npc
./gradlew clean build --no-daemon
```

Windows PowerShell:

```powershell
rin.exe init mod --host fabric --id guide_npc --name "Guide NPC" `
  --namespace io.github.example --author example --output guide_npc
Set-Location guide_npc
.\gradlew.bat clean build --no-daemon
```

The Fabric template pins Minecraft `1.21.1`, Fabric Loader `0.16.14`, Fabric
API `0.116.14+1.21.1`, Loom `1.11.8`, Gradle `8.14.3`, and Java 21. It vendors
the Rin Java SDK and builds a server-side Mod. The Gradle license and notice
files apply to the redistributed Wrapper, not to the generated Mod. Do not silently change one
member of this tested set without repeating the build and real-server gates.

### BepInEx Mono

```bash
rin init mod \
  --host bepinex-mono \
  --id guide_npc \
  --name "Guide NPC" \
  --namespace io.github.example \
  --output guide_npc_mono

cd guide_npc_mono
dotnet restore GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests/bin/Release/net6.0/GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.Mono/GuideNpc.Mono.csproj --locked-mode
dotnet build GuideNpc.Mono/GuideNpc.Mono.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-mono-0.1.0.zip
```

Windows PowerShell uses the same `dotnet` commands:

```powershell
rin.exe init mod --host bepinex-mono --id guide_npc --name "Guide NPC" `
  --namespace io.github.example --output guide_npc_mono
Set-Location guide_npc_mono
dotnet restore GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests\bin\Release\net6.0\GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.Mono\GuideNpc.Mono.csproj --locked-mode
dotnet build GuideNpc.Mono\GuideNpc.Mono.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-mono-0.1.0.zip
```

The template targets `netstandard2.0` and pins the upstream
`6.0.0-be.785` BepInEx 6 Mono package. BepInEx 6 remains an unreleased,
bleeding-edge line; compile success is not evidence that the plugin loads in a
particular Unity game. The packaging helper performs locked publish, requires
`System.Text.Json.dll` and its complete pinned managed dependency set, includes
`LICENSE-RIN.txt` plus the reviewed .NET license/notice set, rejects every DLL
outside the reviewed Mono allowlist, and records every install file checksum in the ZIP
manifest. Verification also enforces the approved SHA-256 of every notice
asset. Adding another managed dependency requires an explicit runtime and
redistribution-license review before changing the allowlist. The helper supports
Python 3.9 and newer.

### BepInEx IL2CPP

```bash
rin init mod \
  --host bepinex-il2cpp \
  --id guide_npc \
  --name "Guide NPC" \
  --namespace io.github.example \
  --output guide_npc_il2cpp

cd guide_npc_il2cpp
dotnet restore GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests/bin/Release/net6.0/GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.IL2CPP/GuideNpc.IL2CPP.csproj --locked-mode
dotnet build GuideNpc.IL2CPP/GuideNpc.IL2CPP.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-il2cpp-0.1.0.zip
```

Windows PowerShell:

```powershell
rin.exe init mod --host bepinex-il2cpp --id guide_npc --name "Guide NPC" `
  --namespace io.github.example --output guide_npc_il2cpp
Set-Location guide_npc_il2cpp
dotnet restore GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests\bin\Release\net6.0\GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.IL2CPP\GuideNpc.IL2CPP.csproj --locked-mode
dotnet build GuideNpc.IL2CPP\GuideNpc.IL2CPP.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-il2cpp-0.1.0.zip
```

The template targets .NET 6 and pins BepInEx
`6.0.0-be.785`. It deliberately does not vendor generated game-specific
Interop assemblies. Install the correct BepInEx build into one concrete game,
let that game generate its Interop files, and implement the owning-thread hook
before attempting an effect. The helper packages the three reviewed project
DLLs plus `LICENSE-RIN.txt`; archive verification rejects every additional DLL,
including BepInEx, Unity, or game-specific Interop runtimes. Extending this
allowlist requires a runtime and redistribution-license review. It intentionally
omits the Mono-only .NET license/notice set because those eight DLLs are not
redistributed.

Both BepInEx package variants use fixed ZIP timestamps, ordering, Unix
regular-file modes, and creator metadata. Verification rejects directory,
symbolic-link, encrypted, overlong, traversal, device-name, case-colliding, and
unmanifested entries before the archive is presented for extraction.

### Luanti

```bash
rin init mod \
  --host luanti \
  --id guide_npc \
  --name "Guide NPC" \
  --author example \
  --output guide_npc

cd guide_npc
luac5.1 -p init.lua
luac5.1 -p state.lua
luac5.1 -p rin.lua
lua5.1 test_state.lua
luac5.4 -p init.lua
luac5.4 -p state.lua
luac5.4 -p rin.lua
lua5.4 test_state.lua
```

On Windows, use the corresponding installed `luac.exe` and `lua.exe` commands.
The generated Mod vendors the Rin Lua SDK and keeps syntax and state tests
compatible with Lua 5.1 and 5.4. Add the generated Mod ID to
`secure.http_mods`, then repeat the lifecycle test in an actual Luanti
headless server. The generator does not write `mod.conf.release`; that field is
owned by ContentDB.

## Required game-specific work

The generated README identifies the following authority boundaries. A Mod is
not ready to distribute until each one has been replaced and reviewed:

1. **Stable save identity.** Derive Session identity from the real world,
   profile, or save slot. Do not use an executable path, process ID, clock, or
   a new random value on every launch.
2. **Action allowlist.** Send only game-authored action IDs and validate every
   returned ID and parameter before apply. Generated text is display data, not
   a command, item ID, reflection target, or filesystem path.
3. **Owning-thread apply.** Marshal Fabric work to the server thread, BepInEx
   work to the game's owning Unity thread, and Luanti work through its scheduled
   server callbacks. Never block a render or server-tick loop on network I/O.
4. **Trusted content binding.** Compute `content_hash` from the running,
   game-owned content manifest. Never copy the expected hash or Binding from an
   imported save, Snapshot, or model response.
5. **Durable workflow recovery.** Persist the complete Pending Turn, accepted
   Job ID, operation marker, and Outcome Outbox. Resume or drain them before
   accepting a new turn. An ambiguous timeout or cancellation must fail closed.
6. **Sidecar lifecycle.** Decide whether the launcher, dedicated server, or game
   installation starts Rin; use a writable per-user data directory, wait for
   health, enforce one writer per data directory, and perform bounded shutdown.
   Keep model-provider credentials in the Sidecar. Pass `RIN_TOKEN` through the
   process environment rather than a save or checked-in Mod config.

The templates intentionally use reversible dialogue, wait, or refusal effects
and remain `advisory`. Item grants, currency, quest advancement, inventory
changes, and world edits require either an operation-keyed idempotent game API
or a real transaction that commits the game effect, applied marker, and
durable outcome together. See
[host durability profiles](host-durability.md).

## Implementation and validation checklist

### Current scaffolding delivery

- [x] Register `init mod`, `--list-hosts`, the four exact host names, and
  actionable help and error output.
- [x] Enforce host-aware ID, display-name, namespace, semantic-version, output,
  Windows-name, case-collision, and symbolic-link validation before writing.
- [x] Embed templates and complete Java, C#, or Lua SDK sources in the Rin
  binary; generate a sorted SHA-256 manifest and preserve required script modes.
- [x] Guarantee no overwrite, no traversal, deterministic dry-run parity, and
  no automatic failure cleanup; retain an incomplete marker for manual review
  without deleting concurrent replacements.
- [x] Build generated Fabric and both BepInEx projects on Linux and Windows;
  build and package each BepInEx backend, independently verify both install
  ZIPs, and parse and exercise Luanti output with Lua 5.1 and 5.4.
- [x] Keep generated READMEs explicit about pinned dependencies, remaining
  game-owned TODOs, Preview status, and the `advisory` capability boundary.

This checklist is an acceptance contract. Check an item only after the
corresponding code and automated test have landed; documentation alone is not
evidence.

### Follow-up real-game validation

- [ ] Load the generated Fabric JAR in a real Minecraft `1.21.1` Dedicated
  Server on Linux and Windows; test two worlds, save/stop, forced termination,
  concurrent players, and Sidecar restart.
- [ ] Load the generated Mono plugin in a named representative game and replace
  the demo save identity and main-thread effect hook.
- [ ] Load the generated IL2CPP plugin in a concrete game after Interop
  generation; test AOT behavior, unload, restart, and an actual game hook.
- [ ] Load the generated Luanti Mod in a real headless server; test
  `secure.http_mods`, real ModStorage save intervals, concurrent players,
  `/shutdown`, and forced termination.
- [ ] Run the shared crash matrix and at least the documented two-hour or
  1,000-turn Preview soak gate for every host/backend claimed by a release.

## Official host references

- [Fabric `fabric.mod.json` specification](https://docs.fabricmc.net/develop/loader/fabric-mod-json)
- [Fabric example Mod](https://github.com/FabricMC/fabric-example-mod)
- [BepInEx 6 plugin setup](https://docs.bepinex.dev/master/articles/dev_guide/plugin_tutorial/1_setup.html)
- [BepInEx IL2CPP installation](https://docs.bepinex.dev/master/articles/user_guide/installation/unity_il2cpp.html)
- [Luanti Mod layout and `mod.conf`](https://api.luanti.org/mods/)
- [Luanti HTTP API](https://docs.luanti.org/for-creators/api/http-api/)
- [Windows file and path naming rules](https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file)
- [Semantic Versioning 2.0.0](https://semver.org/)

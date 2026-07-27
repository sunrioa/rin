# Real-host integration validation

[English](host-integration-validation.md) | [简体中文](host-integration-validation.zh-CN.md)

Rin `0.7.0` is Preview software. Compilation, mocked engine APIs, and
restart-focused unit tests are useful gates, but they do not prove that a Mod
is stable inside a real game. Until the relevant rows below have recorded
evidence, the engine and Mod examples remain `advisory`.

## What CI currently proves

| Integration | Automated evidence | Not yet proved |
| --- | --- | --- |
| Fabric | Real Mod JAR/NBT round trip, official dedicated-server GameTest, and authority matrix | Live Sidecar recovery, multiplayer, forced-stop, and packaged-client integrated-server smoke |
| BepInEx Mono/IL2CPP | Real BepInEx package compilation and Core restart tests | Plugin load, game hooks, save identity, and shutdown in representative games |
| Luanti | Lua 5.1/5.4 tests plus official 5.16.1 LuaJIT Dedicated Server restart tests with real ModStorage on macOS/Windows | Live Sidecar traffic, concurrent players, forced termination, map-save timing, and soak |
| Godot | Official 4.6.3 headless authority generations, exact Offer binding, Active Run recovery, restart, and file-failure tests on Linux/Windows | Live Sidecar traffic in an editor session and exported build |
| Unity | Strict API stubs: Scene/Domain generations, NavMesh compile, cancellation, late callbacks, Active Run/opaque-argument recovery, and Windows-safe replacement | Unity Editor package import and Mono/IL2CPP Player builds |
| Unreal | Runtime Plugin structure, forbidden-surface, and Windows path tests | Unreal Header Tool/compiler, Editor load, packaged builds, SaveGame and navigation runtime |
| Ren'Py | Python adapter/Epoch tests; local Ren'Py 8.5.3 lint and rollback harness | Visible engine save/load, interaction restart, and packaged builds |
| OpenSpiel | Real 2.0.1 sequential/simultaneous/chance/hidden-information games on macOS/Linux/Windows | Semantic oracle only; no engine thread, save, Sidecar, or long-world-action lifecycle |
| Terminal Story | Real Sidecar 20-turn CI on Windows, macOS, and Linux | It is a reference game, not evidence for another game's Mod lifecycle |

## Shared crash and recovery matrix

The checked-in `rin.host-scenarios/v1` contract indexes executable evidence
for stale Epoch rejection, stable Operation idempotency, dynamic capability
revocation, exact Outbox retry, long-action Epoch cancellation, authority
thread nonblocking, recovery cleanup, simultaneous decisions, host-owned
chance, and hidden-information noninterference. A scenario entry proves only
the listed evidence files and their CI runners; it does not imply every engine
has passed every case. Host-specific gaps below remain manual release gates.

Run every applicable case against a copy of a real save. Kill the game or
Sidecar at the named boundary; do not substitute an exception thrown inside a
unit test.

1. After persisting a Pending Turn, before sending its request.
2. After the Sidecar accepts a request, while its response is lost.
3. Before and after persisting an asynchronous Job ID, including Sidecar
   restart while polling.
4. After applying the game effect, before persisting its operation marker or
   Outcome Outbox entry. An `advisory` integration may expose this duplication
   window; promotion requires a game transaction or an idempotent operation
   primitive that closes it.
5. After sending an outcome while its acknowledgement is lost.
6. During temporary-file/backup replacement and during the host's normal
   autosave.
7. With the Sidecar absent, started late, restarted, and unavailable during
   orderly game shutdown.

For every restart, verify that request and event IDs remain stable, no turn
overlaps another turn, an already applied operation is not applied twice,
unresolved work remains recoverable, and the Outcome Outbox eventually drains.

## Host-specific gates

### Fabric

- The official GameTest starts a real Minecraft Dedicated Server and checks
  lifecycle binding plus server-thread dispatch. Unit tests cover integrated
  and dedicated classification, persisted generations, and stale Epoch
  rejection; metadata must remain `environment: "*"`.
- Install the built JAR in the pinned Minecraft `1.21.1` Fabric Dedicated
  Server and verify startup, command/event hooks, and server-thread access.
- Exercise two different worlds, reopen the same world, use `save-all flush`,
  normal `/stop`, forced termination, and at least two concurrent players.
- Confirm the save/world identity is not shared across worlds and recovery
  state follows the authoritative world save. Run on Windows as well as Linux.
- Add Fabric GameTests for deterministic gameplay behavior; retain a real
  server smoke test for lifecycle and packaging. Quick-play a singleplayer
  world on each target OS and confirm the log binds `integrated` authority.

### BepInEx

- Treat BepInEx 6 as bleeding-edge/unreleased and pin the exact runtime build.
- For Mono, load the DLL in one named representative game, source
  `SaveIdentity` from the actual save/profile, verify main-thread effect
  application, dependency resolution, quit, and restart.
- For IL2CPP, repeat in a concrete game after its first-run interop generation.
  Replace the example `ApplyDialogue` delegate with a real game hook and test
  AOT/native backend behavior. A build against generic packages is not enough.

### Luanti

- A real Luanti headless server—the official 5.16.1 Dedicated Server—now
  loads the source Mod and a newly generated standalone scaffold twice against
  one real world. The test uses
  real ModStorage userdata, runs the SDK/state suites in engine LuaJIT, keeps
  World identity stable, advances Host/Timeline generations, and is repeated
  in Windows CI with a SHA-256-pinned official ZIP.
- Keep `secure.http_mods` configured. Test real ModStorage across map-save
  intervals, `/shutdown`, forced termination, and world reopen; the automated
  graceful restart is not evidence for these failure boundaries.
- Exercise simultaneous players, slow/unavailable Sidecar responses, and the
  platform's loopback/redirect policy on Windows and Linux.

### Godot

- Run an actual scene against a live Sidecar in the editor and in exported
  Windows and Linux builds.
- Verify `user://` persistence, scene reload, application exit, network
  partition, UI responsiveness, and that callbacks touching nodes return to
  the main thread.

### Unity

- Import the package through Unity Package Manager at the declared minimum
  `2021.3` API level and at every Unity version the project intends to claim.
- Build and run Windows Mono and IL2CPP Players. Test scene/domain reload,
  `Application.persistentDataPath`, stripping/AOT, coroutine/main-thread
  behavior, application quit, and the shared crash matrix.
- Start the NavMesh action, change scene while path following, reload scripts
  in the Editor, and destroy the Host. Confirm one terminal
  `cancelled`/`outcome-unknown` report, unchanged raw arguments, and no effect
  from a late callback. Repeat past the authored deadline.

### Unreal

- Copy `examples/unreal/RinHost` into a real project's `Plugins` directory and
  build with the project's exact Unreal Engine version. Run Unreal Header Tool,
  load the Editor, and package Development and Shipping Windows builds.
- Restore stable Session/Host/World/Timeline generations from a real SaveGame;
  test PIE instances, server travel, seamless and non-seamless map changes,
  save/load, shutdown, forced termination, and world reopen.
- Run the Behavior Tree movement example on the authoritative game thread.
  Cancel during path following, unload the World while running, and verify
  late callbacks cannot revive an old Epoch or duplicate an operation.
- Replace the bounded in-memory markers with a SaveGame/database transaction
  before claiming idempotent or transactional durability.

### Ren'Py

- Run inside the actual engine and verify save/load, rollback, interaction
  restart, and clean shutdown.
- Bind stable game-owned save/world IDs, then confirm a loaded older save and
  the first interaction after rollback both increase Timeline above the
  persistent high-water mark. Confirm old worker completion becomes
  `stale_epoch`.
- Confirm that serialized state contains plain recovery data, not live worker,
  socket, lock, or callback objects.

## Soak and release evidence

As a recommended Preview release gate, run at least two hours or 1,000 turns
per claimed host/backend while injecting timeouts, connection resets, Sidecar
restarts, and game restarts. This is a repeatable minimum, not proof that every
game is stable. Require:

- no unbounded thread, task, handle, memory, recovery-file, or outbox growth;
- no duplicate world effect and no permanently overlapping turn;
- eventual recovery or an explicit terminal error for every accepted turn;
- no credential, full player text, or save payload in normal logs;
- no unacceptable frame or server-tick stall under the game's own budget.

Record the Rin commit and artifact hash, exact game/loader/engine/backend and
versions, OS and filesystem, complete Mod list, source of the save identity,
test/crash point, expected and actual result, relevant sanitized logs, and
remaining Pending Turn/Attempt/Outbox counts.

Only promote host durability after this evidence is reviewed. Call an
operation `idempotent` only after the same operation ID has been repeated
without repeating its game effect. Call it `transactional` only when the game
effect, operation marker, and durable outcome are committed by one real
transaction.

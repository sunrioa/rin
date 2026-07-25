# Fabric Rin NPC example

[English](README.md) | [简体中文](README.zh-CN.md)

A buildable server-side reference for Minecraft `1.21.1`, Fabric Loader
`0.16.14`, Fabric API `0.116.14+1.21.1`, Loom `1.11.8`, Gradle `8.14.3`, and
Java 21. These versions are deliberately pinned and tested together; this is
not a claim of unchanged compatibility with every future Minecraft release.

## Build and install

Linux/macOS:

```bash
./gradlew clean build
```

Windows PowerShell or Command Prompt:

```bat
gradlew.bat clean build
```

Copy `build/libs/rin-fabric-npc-0.6.0.jar` plus the matching Fabric API JAR to
the dedicated server's `mods` directory. Start Rin, optionally set `RIN_URL`
and `RIN_TOKEN` in the server process environment, then run `/rin-npc ask` as
a player. The Mod JAR includes the Rin Java client classes; do not install a
second SDK JAR.

## Safety and recovery model

**Host capability profile: `advisory` with stable identity.** The Mod stores a
generated world UUID, stable sequence, exact Create/Observe/Propose requests,
Pending Turn/Job identity, and a bounded Outcome Outbox in overworld Saved
Data. Restarting the same save resumes retained work instead of creating a new
Session. Each Outbox entry retains the exact Commit and a pre-recorded
absolute-fact Observe fallback. Only explicit terminal Commit errors permit
that persisted conversion.

`PersistentState.markDirty()` schedules a later save; it is not a synchronous
durable-before-network barrier and cannot atomically combine a game mutation
with Outbox persistence. This reference therefore offers only reversible
chat/wait/refuse actions and truthfully remains `advisory`. Item grants, quest
changes, and world edits require a proven idempotent or transactional host
boundary described in
[Host capability profiles](../../../docs/host-capability-profiles.md).

Immediately before settlement, the Mod reloads Rin Session state and the Java
SDK checks that the Proposal remains pending at the expected revision.
Missing, stale, malformed, or unavailable state fails closed. The game selects
only a local allowlisted action ID; model text never becomes a command, item
ID, reflection target, or world edit. Minecraft access and Saved Data mutation
are marshalled through `MinecraftServer.execute`.

The host orchestration entry is 250 lines (down from 1,046). Authored protocol
payloads, Saved Data, the `WorkflowStore`, and server-thread dispatch are
separate bounded classes, so a game author can review or replace each boundary
without copying the SDK state machine.

Saved state is bounded to 256 sessions, 32 reports per session, and 2,000,000
JSON characters. Reaching a bound stops new work instead of silently discarding
recovery data. Back up the world before upgrading this Preview example and
keep the Mod and Sidecar pinned to the same Rin revision.

References: [Fabric example mod](https://github.com/FabricMC/fabric-example-mod),
[project structure](https://docs.fabricmc.net/develop/getting-started/project-structure),
and [Saved Data](https://docs.fabricmc.net/develop/serialization/saved-data).

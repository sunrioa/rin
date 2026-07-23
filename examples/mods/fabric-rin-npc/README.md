# Fabric Rin NPC example

This is a source overlay for a dedicated-server Fabric mod, not a frozen
Gradle template. Start from the current official Fabric project generator so
Minecraft, Loader, mappings, Fabric API, and Loom stay on compatible versions.

1. Generate a Java 21 / Minecraft 1.21+ Fabric project.
2. Copy this example's `src` directory into it.
3. Copy `sdk/java/src/main/java/io/github/sunrioa/rin` into the generated
   project's `src/main/java/io/github/sunrioa/rin` directory.
4. Start Rin and set optional `RIN_URL` / `RIN_TOKEN` environment variables.
5. Run the server and enter `/rin-npc ask` as a player.

The command creates an isolated sample session, observes the interaction,
submits an asynchronous proposal job, validates one of three action IDs, then
uses `MinecraftServer.execute` to apply it on the server thread. The result is
committed only after application. Replace the chat-only `switch` with your own
NPC API; do not let model text directly invoke commands, item grants, or world
edits.

Reference template: https://github.com/FabricMC/fabric-example-mod
Project structure: https://docs.fabricmc.net/develop/getting-started/project-structure

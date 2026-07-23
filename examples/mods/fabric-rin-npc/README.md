# Fabric Rin NPC example

[English](README.md) | [简体中文](README.zh-CN.md)

A minimal server-side integration template for the Rin agent runtime.

This directory is a source overlay, not a frozen Gradle template. Start from
the current official project generator so the game, loader, mappings, API, and
build plugin stay on compatible versions.

1. Generate a Java 21 / Minecraft 1.21+ Fabric project.
2. Copy this example's `src` directory into it.
3. Copy `sdk/java/src/main/java/io/github/sunrioa/rin` into the generated
   project's `src/main/java/io/github/sunrioa/rin` directory.
4. Start Rin and set optional `RIN_URL` / `RIN_TOKEN` environment variables.
5. Run the server and enter `/rin-npc ask` as a player.

The command creates an isolated `outcome-reporting-v1` session, observes the
interaction, and submits an asynchronous proposal job. Immediately before
apply it reads Session state again: the proposal must still be `pending`, and
its world revision (or, for a non-world proposal, creation revision) must still
match. Stale proposals are rejected without a game effect. The allowlisted
result is applied with `MinecraftServer.execute`, and its actual server tick is
captured at that accept/reject decision. Replace the chat-only `switch` with
your own NPC API; never let model text directly invoke commands, item grants,
or world edits.

The complete Create payload (including request ID and seed) stays stable across
ambiguous retries. If Rin is unavailable before any online proposal exists,
the game may apply one explicit authored offline fallback. Once a proposal
exists, State/read errors fail closed. A retained Outbox entry always blocks a
new turn until it can be flushed. Before submitting, the mod also retains the
complete Propose request and, after `202`, its Job ID. An unresolved attempt is
resumed on the next command without advancing the sequence or choosing a
fallback; it is removed only in the game transaction that stores the effect,
applied marker, and Outbox entry. Either retained state blocks a new turn.

This source-only sample keeps applied operations and the Outbox in memory. A
Commit entry also contains a safe Observe fallback made only of memory and an
absolute fact. Temporary Commit errors retain the exact Commit; only explicit
terminal errors such as `unknown_proposal` atomically convert it to Observe.
Durable ACK/delete must succeed before eviction. Replace all marked persistence
hooks with one fallible authoritative world/player-data transaction covering
the game effect, applied marker, both report payloads, retained Create/Propose
requests, optional Job ID, and session/sequence state. The demo removes
marker/outbox state when its effect callback throws, but only a real game save
transaction can roll back a partial world mutation.

Reference template: https://github.com/FabricMC/fabric-example-mod
Project structure: https://docs.fabricmc.net/develop/getting-started/project-structure

# BepInEx Rin NPC example

[English](README.md) | [简体中文](README.zh-CN.md)

A minimal plugin integration template for the Rin agent runtime.

This source overlay targets BepInEx 6 on a modern Unity/.NET runtime.

1. Create a plugin from the official BepInEx plugin template for the target
   game's backend and framework version.
2. Add a project reference to `sdk/csharp/Rin.Client/Rin.Client.csproj`, or
   copy its compiled assembly into the plugin's reference directory.
3. Add `Plugin.cs`, start Rin, and build the plugin into `BepInEx/plugins`.
4. Configure only `BaseUrl` in the generated BepInEx config. Supply a remote
   bearer token through the `RIN_TOKEN` process environment variable.
5. Press F8 for the isolated demo turn, or call `RequestNpcTurn` from the
   target game's actual dialogue or interaction hook.

`Update` only drains a bounded main-thread queue and detects the optional demo
key. HTTP runs asynchronously. The plugin opts into `outcome-reporting-v1` and
re-reads Session immediately before apply. The proposal must still be
`pending`, with a matching world revision (or creation revision for a
non-world proposal); otherwise the game rejects it without an effect. The
plugin validates `talk`, `wait`, or `refuse`, invokes `NpcActionReady` on
Unity's main thread, and captures `Time.frameCount` at the actual accept/reject.
A real game-specific plugin should map those IDs to its own NPC APIs.

The complete Create payload, request ID, and seed remain unchanged across
retries. If Rin is unavailable before any online proposal exists, the plugin
may run one explicit game-authored fallback. State failures after an online
proposal fail closed. Before submitting, the plugin retains the complete
Propose request and, after `202`, its Job ID. An unresolved attempt is resumed
on the next interaction without advancing the sequence or choosing a fallback;
it is removed only in the game transaction that stores the effect, applied
marker, and Outbox entry. Either a retained attempt or an Outbox entry blocks
every new turn.

This source-only sample stores applied operations and Outbox entries in memory.
Each Commit also stores a safe Observe fallback containing only memory and an
absolute fact. Temporary errors retain the exact Commit; only explicit terminal
errors such as `unknown_proposal` atomically convert it to Observe. Durable
ACK/delete must succeed before eviction. Replace the marked hooks with one
fallible game-save transaction covering the effect, marker, both report
payloads, retained Create/Propose requests, optional Job ID, and
session/sequence state. In particular, an `NpcActionReady`
subscriber exception must roll the transaction back and leave no accepted
marker or Outbox entry; only the target game's real transaction can also undo a
subscriber's partial world mutation.

Official plugin tutorial: https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/index.html
Configuration guide: https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/4_configuration.html

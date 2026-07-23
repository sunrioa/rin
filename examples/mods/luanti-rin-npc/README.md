# Luanti Rin NPC example

[English](README.md) | [简体中文](README.zh-CN.md)

A minimal server-side integration template for the Rin agent runtime.

This directory is a complete Luanti mod. The included `rin.lua` is a vendored
copy of `sdk/lua/rin.lua`; the repository test requires both copies to match.

1. Copy this directory to the Luanti `mods` or world `worldmods` directory.
2. Add `rin_npc_example` to `secure.http_mods` in `minetest.conf`.
3. Start Rin at `http://127.0.0.1:7374`, enable the mod, and restart the world.
4. Run `/rin_npc` or `/rin_npc your message` in chat.

The mod calls `core.request_http_api()` only at module scope, keeps the returned
API local, uses `HTTPApiTable.fetch` asynchronously, and schedules polling with
`core.after`. It opts into `outcome-reporting-v1`, then re-reads Session
immediately before apply. The proposal must still be `pending`, with a matching
world revision (or creation revision for a non-world proposal). Stale
proposals are rejected without a game effect. It maps only `talk`, `wait`, and
`refuse` to fixed game-owned effects, and captures Luanti's monotonic game tick
at the actual accept/reject.

The complete Create payload, request ID, and seed remain unchanged across
retries. If Rin is unavailable before any online proposal exists, the mod may
run one explicit authored fallback. State failures after a proposal fail
closed. Before submitting, the mod retains the complete Propose request and,
after `202`, its Job ID. An unresolved attempt is resumed on the next command
without advancing the turn sequence or choosing a fallback; it is removed only
in the game transaction that stores the effect, applied marker, and Outbox
entry. Either a retained attempt or an Outbox entry blocks a new turn.

This source sample keeps applied operations and Outbox entries in memory. Each
Commit stores a safe Observe fallback containing only memory and an absolute
fact. Temporary errors retain the exact Commit; only explicit terminal errors
such as `unknown_proposal` atomically convert it to Observe. Durable ACK/delete
must succeed before eviction. Implement all marked hooks as one fallible
game/ModStorage transaction covering the effect, marker, both report payloads,
retained Create/Propose requests, optional Job ID, and sequence. The demo
removes marker/outbox state when its effect callback throws, but only a real
game transaction can undo an already-partial world mutation.

Luanti's HTTP implementation follows redirects and the Lua API provides no
per-request switch to disable that behavior. For that reason this example
accepts only explicit loopback HTTP origins and refuses Authorization headers;
do not adapt it to an authenticated remote Rin endpoint without a stricter
native transport.

Official HTTP API: https://docs.luanti.org/for-creators/api/http-api/
Official Lua API source: https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md

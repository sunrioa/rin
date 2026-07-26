# Luanti Rin NPC example

[English](README.md) | [简体中文](README.zh-CN.md)

A server-side integration reference for the Rin agent runtime.

**Host durability profile: `advisory`.** ModStorage snapshots are tied to the
world save interval and cannot provide the synchronous transaction required by
the stronger profiles. This sample persists recovery state, but does not claim
that a successful `set_string` is crash-durable or atomic with a game effect. See
[Host durability profiles](../../../docs/host-durability.md).

This directory is a complete Luanti mod. The included `rin.lua` is a vendored
copy of `sdk/lua/rin.lua`; the repository test requires both copies to match.
`state.lua` is the bounded ModStorage adapter, while `init.lua` contains only
Luanti transport, session wiring, game-owned action policy, and UI behavior.

1. Copy this directory to the Luanti `mods` or world `worldmods` directory.
2. Add `rin_npc_example` to `secure.http_mods` in `minetest.conf`.
3. Start Rin at `http://127.0.0.1:7374`, enable the mod, and restart the world.
4. Run `/rin_npc` or `/rin_npc your message` in chat.

The mod calls `core.request_http_api()` only at module scope, keeps the returned
API local, uses `HTTPApiTable.fetch` asynchronously, and schedules polling with
`core.after`. It re-reads Session immediately before apply. The proposal must
still be pending, with a matching
world revision (or creation revision for a non-world proposal). Stale
proposals are rejected without a game effect. It maps only `talk`, `wait`, and
`refuse` to fixed game-owned effects, and captures Luanti's monotonic game tick
at the actual accept/reject.

`state.lua` persists a generated world identity, per-player Session/Create
identity, monotonic logical tick floor, sequence, Pending Observe, complete
Pending Turn, Job ID, and Outcome Outbox. State is limited to 1 MiB, 128
players, and 64 outcomes. It validates loaded snapshots and publishes a
copy-on-write in-memory state only after ModStorage accepts the encoded
candidate. Player names are hashed into Session IDs, avoiding normalization
collisions.

The Lua SDK Workflow owns submit/poll/recovery, Job identity checks, terminal
no-proposal handling, freshness evaluation, and Outbox draining. It stores the
Pending Turn before the first request and the Job ID before the first GET.
After restart, the next command reuses the same request and Job; a confirmed
missing Job may be resubmitted once. Every report error retains the exact
Action Report for retry; it is never converted into an Observation.

If Rin is unavailable, the mod fails closed or retains work. Because
Luanti cannot atomically combine an arbitrary world mutation with ModStorage,
a process crash between the chat effect and state publication can still repeat
that effect. A production game with a transactional database should implement
the same Workflow Store contract inside its authoritative transaction before
claiming a stronger profile.

Luanti's HTTP implementation follows redirects and the Lua API provides no
per-request switch to disable that behavior. For that reason this example
accepts only explicit loopback HTTP origins and refuses Authorization headers;
do not adapt it to an authenticated remote Rin endpoint without a stricter
native transport.

Official HTTP API: https://docs.luanti.org/for-creators/api/http-api/
Official Lua API source: https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md

Repository verification runs the SDK and restart/write-failure state harness
on both Lua 5.1 and Lua 5.4. It is a faithful ModStorage boundary harness, not
a substitute for a game-specific Luanti headless integration test.

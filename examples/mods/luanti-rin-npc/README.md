# Luanti Rin NPC example

This is a complete server-side Luanti mod. The included `rin.lua` is a vendored
copy of `sdk/lua/rin.lua`; the repository test requires both copies to match.

1. Copy this directory to the Luanti `mods` or world `worldmods` directory.
2. Add `rin_npc_example` to `secure.http_mods` in `minetest.conf`.
3. Start Rin at `http://127.0.0.1:7374`, enable the mod, and restart the world.
4. Run `/rin_npc` or `/rin_npc your message` in chat.

The mod calls `core.request_http_api()` only at module scope, keeps the returned
API local, uses `HTTPApiTable.fetch` asynchronously, and schedules polling with
`core.after`. It maps only `talk`, `wait`, and `refuse` to fixed game-owned
effects before committing the result.

Luanti's HTTP implementation follows redirects and the Lua API provides no
per-request switch to disable that behavior. For that reason this example
accepts only explicit loopback HTTP origins and refuses Authorization headers;
do not adapt it to an authenticated remote Rin endpoint without a stricter
native transport.

Official HTTP API: https://docs.luanti.org/for-creators/api/http-api/
Official Lua API source: https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md

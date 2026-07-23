# Luanti Rin NPC 示例

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Rin 智能体运行时的最小服务端接入模板。

该目录是完整的 Luanti 服务器 Mod。内置 `rin.lua` 是 `sdk/lua/rin.lua`
的 Vendored Copy；仓库测试要求两份文件完全一致。

1. 把该目录复制到 Luanti `mods` 或世界 `worldmods` 目录。
2. 在 `minetest.conf` 中把 `rin_npc_example` 加入 `secure.http_mods`。
3. 在 `http://127.0.0.1:7374` 启动 Rin，启用 Mod 并重启世界。
4. 在聊天中执行 `/rin_npc` 或 `/rin_npc your message`。

Mod 只在模块作用域调用 `core.request_http_api()`，把返回 API 保持为 local，
通过 `HTTPApiTable.fetch` 异步请求，并用 `core.after` 调度轮询。它只把
`talk`、`wait` 和 `refuse` 映射到游戏拥有的固定效果，再 Commit 结果。

Luanti HTTP 实现会跟随重定向，而 Lua API 没有单请求关闭开关。因此示例
只接受显式 loopback HTTP Origin，并拒绝 Authorization Header；没有更
严格的原生 Transport 时，不要把它改为连接经过鉴权的远程 Rin。

官方 HTTP API：https://docs.luanti.org/for-creators/api/http-api/

官方 Lua API 源码：https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md

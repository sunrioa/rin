# Luanti Rin NPC 示例

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Rin 智能体运行时的服务端接入参考。

**Host capability profile：`advisory`。** ModStorage Snapshot 取决于世界保存
间隔，不能提供更强 Profile 所需的同步事务。示例会持久化恢复状态，但不会把
成功的 `set_string` 宣称为 Crash-durable，也不会声称它与游戏效果原子。参见
[宿主能力分级](../../../docs/host-capability-profiles.zh-CN.md)。

该目录是完整的 Luanti 服务器 Mod。内置 `rin.lua` 是 `sdk/lua/rin.lua`
的 Vendored Copy；仓库测试要求两份文件完全一致。`state.lua` 是有界
ModStorage Adapter，`init.lua` 只保留 Luanti Transport、Session Wiring、
游戏拥有的动作 Policy 和 UI 行为。

1. 把该目录复制到 Luanti `mods` 或世界 `worldmods` 目录。
2. 在 `minetest.conf` 中把 `rin_npc_example` 加入 `secure.http_mods`。
3. 在 `http://127.0.0.1:7374` 启动 Rin，启用 Mod 并重启世界。
4. 在聊天中执行 `/rin_npc` 或 `/rin_npc your message`。

Mod 只在模块作用域调用 `core.request_http_api()`，把返回 API 保持为 local，
通过 `HTTPApiTable.fetch` 异步请求，并用 `core.after` 调度轮询。它启用
`outcome-reporting-v1`，应用前重新读取 Session；Proposal 必须仍是
`pending`，而且 World Revision（非世界 Proposal 则为创建 Revision）必须
匹配。过期 Proposal 不产生游戏效果，只报告 Rejected。Mod 只把 `talk`、
`wait` 和 `refuse` 映射到游戏拥有的固定效果，并在实际 Accept/Reject 时
读取 Luanti 单调游戏 Tick。

`state.lua` 会保存生成的 World Identity、每玩家 Session/Create Identity、
单调逻辑 Tick 下限、Sequence、Pending Observe、完整 Pending Turn、Job ID
与 Outcome Outbox。状态上限为 1 MiB、128 名玩家和 64 条 Outcome；载入时
执行完整校验，并且只在 ModStorage 接受编码后的候选状态后，才以 Copy-on-write
方式发布内存状态。玩家名经 Hash 写入 Session ID，避免规范化碰撞。

Lua SDK Workflow 负责 Submit/Poll/Recovery、Job Identity 检查、终态无
Proposal 处理、Freshness 与 Outbox Drain。首次请求前先保存 Pending Turn，
首次 GET 前保存 Job ID。重启后的下一条命令复用同一 Request 和 Job；只有
确认 Job 不存在时才允许一次 Resubmit。每条 Commit 都保留安全 Observe
Fallback；临时报告错误保留原 Entry，明确的 Commit 终态错误会先持久转换为
Observe 再重试。

只有 Rin 尚未产生在线 Proposal 的冷启动不可用场景，Mod 才可能应用一条明确
编写的 Offline Fallback；后续失败会 Fail Closed 或保留待办。由于 Luanti
无法把任意世界修改与 ModStorage 合并为原子事务，进程若恰好在聊天效果与
状态发布之间崩溃，该效果仍可能重复。使用事务数据库的生产游戏应在自己的
权威事务内实现同一 Workflow Store Contract，之后才能声明更强 Profile。

Luanti HTTP 实现会跟随重定向，而 Lua API 没有单请求关闭开关。因此示例
只接受显式 loopback HTTP Origin，并拒绝 Authorization Header；没有更
严格的原生 Transport 时，不要把它改为连接经过鉴权的远程 Rin。

官方 HTTP API：https://docs.luanti.org/for-creators/api/http-api/

官方 Lua API 源码：https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md

仓库会在 Lua 5.1 与 Lua 5.4 下执行 SDK 测试及重启/写失败状态 Harness。该
Harness 忠实模拟 ModStorage 边界，但不能替代具体游戏的 Luanti Headless
集成测试。

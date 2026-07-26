# Luanti Rin NPC 示例

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Rin 智能体运行时的服务端接入参考。

**Host durability profile：`advisory`。** ModStorage Snapshot 取决于世界保存
间隔，不能提供更强 Profile 所需的同步事务。示例会持久化恢复状态，但不会把
成功的 `set_string` 宣称为 Crash-durable，也不会声称它与游戏效果原子。参见
[宿主持久保证分级](../../../docs/host-durability.zh-CN.md)。

该目录是完整的 Luanti 服务器 Mod。内置 `rin.lua` 是 `sdk/lua/rin.lua`
的 Vendored Copy；仓库测试要求两份文件完全一致。`state.lua` 是有界
ModStorage Adapter，`init.lua` 只保留 Luanti Transport、Session Wiring、
游戏拥有的动作 Policy 和 UI 行为。

1. 把该目录复制到 Luanti `mods` 或世界 `worldmods` 目录。
2. 在 `minetest.conf` 中把 `rin_npc_example` 加入 `secure.http_mods`。
3. 在 `http://127.0.0.1:7374` 启动 Rin，启用 Mod 并重启世界。
4. 在聊天中执行 `/rin_npc` 或 `/rin_npc your message`。

Mod 只在模块作用域调用 `core.request_http_api()`，把返回 API 保持为 local，
通过 `HTTPApiTable.fetch` 异步请求，并用 `core.after` 调度轮询。应用前会重新
读取 Session；Proposal 必须仍是 `pending`、Revision 匹配，而且 Actor、Tick、
完整 Decision Window 与完整 Action 必须精确匹配持久的宿主 Offer。过期或被
替换的 Proposal 不产生游戏效果，只报告 Rejected。Mod 只把 `talk`、`wait`
和 `refuse` 映射到游戏拥有的固定效果。

`state.lua` 接受真实 Luanti ModStorage userdata，并保存宿主提供的 Content Binding、
生成的 World Identity、Host/World/Timeline Generation、每玩家 Session/Create
Identity、单调逻辑 Tick 下限、Sequence、Pending Observe、完整 Pending Turn、
Active Run、Job ID 与 Outcome Outbox。状态上限为 1 MiB、128 名玩家和 64 条
Outcome。格式哨兵会保留空持久集合，因为 `core.write_json` 会把空 Lua Table
转换成 JSON `null`。玩家名经 Hash 写入 Session ID，避免规范化碰撞。
仓库中的全零 Content Hash 是明确的脚手架占位符；真实 Mod 必须用可信内容
Manifest 计算出的 Hash 替换它。

Lua SDK Workflow 负责 Submit/Poll/Recovery、Job Identity 检查、终态无
Proposal 处理、Freshness 与 Outbox Drain。首次请求前先保存 Pending Turn，
首次 GET 前保存 Job ID。重启后的下一条命令复用同一 Request 和 Job；只有
确认 Job 不存在时才允许一次 Resubmit。调用游戏代码前先保存 Accepted Active
Run；Server 重启会把它一次性对账成 `outcome-unknown`，不会盲目重复效果。
任何报告错误都会保留精确 Action Report 供重试，绝不会转换为 Observation。

Rin 不可用时 Mod 会 Fail Closed 或保留待办。由于 Luanti 无法把任意世界修改
与 ModStorage 合并为原子事务，框架不能证明被中断的效果是否发生。使用事务
数据库的生产游戏应在自己的权威事务内实现同一 Workflow Store Contract，
之后才能声明更强 Profile。

Luanti 无法区分空 Lua Object 与空 Lua Array，`core.write_json` 会把两者都
写成 `null`。SDK 因此拒绝歧义空 Table，不会发送非法协议 JSON。可选空 Array
应省略，每个动作参数 Object 至少包含一个宿主编写字段。

Luanti HTTP 实现会跟随重定向，而 Lua API 没有单请求关闭开关。因此示例
只接受显式 loopback HTTP Origin，并拒绝 Authorization Header；没有更
严格的原生 Transport 时，不要把它改为连接经过鉴权的远程 Rin。

官方 HTTP API：https://docs.luanti.org/for-creators/api/http-api/

官方 Lua API：https://api.luanti.org/core-namespace-reference/

仓库会在 Lua 5.1/5.4 与官方 Luanti 5.16.1 LuaJIT 内执行 SDK 和状态 Harness。
真实 Dedicated Server 会在 macOS 对同一世界加载源码 Mod 与生成的独立脚手架
各两次；Windows CI 使用 SHA-256 固定的官方 Release 重复源码 Mod 生命周期。

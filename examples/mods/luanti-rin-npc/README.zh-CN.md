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
通过 `HTTPApiTable.fetch` 异步请求，并用 `core.after` 调度轮询。它启用
`outcome-reporting-v1`，应用前重新读取 Session；Proposal 必须仍是
`pending`，而且 World Revision（非世界 Proposal 则为创建 Revision）必须
匹配。过期 Proposal 不产生游戏效果，只报告 Rejected。Mod 只把 `talk`、
`wait` 和 `refuse` 映射到游戏拥有的固定效果，并在实际 Accept/Reject 时
读取 Luanti 单调游戏 Tick。

完整 Create Payload、Request ID 和 Seed 在所有重试间不变。只有 Rin 尚未
生成在线 Proposal 的冷启动不可用场景，Mod 才运行一个明确编写的 Offline
Fallback；已有 Proposal 后的 State 失败必须 Fail Closed。Outbox 仍有
Entry 时禁止开始新 Turn。Mod 会在提交前保留完整 Propose Request，并在收到
`202` 后立即保存 Job ID；未决 Attempt 会在下一次命令中用同一身份恢复，不
增长 Turn Sequence，也不选择 Fallback。只有游戏效果、Applied Marker 与
Outbox 在同一事务中落盘时才移除 Attempt；未决 Attempt 与 Outbox 都会阻止
新 Turn。

本源码示例只在内存保存 Applied Operation 与 Outbox。每条 Commit 同时
保存一个只含 Memory 与绝对 Fact 的安全 Observe 降级载荷；临时错误保留
原 Commit，只有 `unknown_proposal` 等明确终态错误才原子转换。Durable
ACK/Delete 成功后才能 Evict。用于持久世界前，应把所有标记 Hook 实现为
可失败的权威游戏/ModStorage 事务，同时包住效果、Marker、两份报告载荷、
保留的 Create/Propose Request、可选 Job ID 与 Sequence。示例会在效果
Callback 抛错时移除 Marker/Outbox，但只有真实游戏事务才能回滚已部分写入
的世界状态。

Luanti HTTP 实现会跟随重定向，而 Lua API 没有单请求关闭开关。因此示例
只接受显式 loopback HTTP Origin，并拒绝 Authorization Header；没有更
严格的原生 Transport 时，不要把它改为连接经过鉴权的远程 Rin。

官方 HTTP API：https://docs.luanti.org/for-creators/api/http-api/

官方 Lua API 源码：https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md

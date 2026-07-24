# Rin SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 `rin.protocol/v1` HTTP 边界的轻量、源码优先客户端。

这些 SDK 消除传输样板代码，不会把游戏权威移动到客户端库。

| 语言 | 运行时 | JSON | 异步建议 |
| --- | --- | --- | --- |
| Python | 3.9+ | 标准库 | 实时游戏从 Worker 调用 |
| JavaScript | Node 18+ / 现代浏览器宿主 | 内置 | 基于 Promise |
| C# | .NET 6+ | `System.Text.Json` | 基于 `Task` |
| Java | 17+ | 宿主提供 JSON 文本 | 基于 `CompletableFuture` |
| Lua | 5.1+ 宿主 | 注入 Codec 与 Transport | 基于 Callback |

所有客户端遵循以下规则：

- 只对显式 loopback Origin 接受明文 HTTP；
- 远程 Origin 要求 HTTPS 和 Bearer Token；
- 拒绝重定向；
- 强制请求超时和响应大小限制；
- 错误只暴露有界 Rin Code，不暴露供应商正文或凭据；
- 调用方负责生成并持久保存每个 Session mutation 的 `request_id` 以及
  Observe/Outcome 的 `event_id`；SDK 不会生成、轮换或静默替换它们；
- SDK 不会自动重试 mutation。调用方只能重试完全相同的 typed payload 和 ID；
  同一 Request ID 下改变任一字段都会返回 `request_id_conflict`；
- 完全相同的 duplicate 会返回首次持久 revision/head（或原始
  Proposal/Arbitration）并设置 `duplicate=true`；需要当前 head 时应读取
  Session State；
- `event_exists` 是其他请求造成的冲突，不是 duplicate 确认；
- Proposal 保持 Pending，直到游戏应用或拒绝后用 Commit 回报结果；Commit
  是结果记账，不是执行授权。

收到 `mutation_outcome_unknown` 后，应保留非 Proposal Operation，并且只用
其完全相同的 typed payload 与 ID 重试；该 mutation 可能已经持久化，其他
Session mutation 会阻塞到确认完成。Proposal 写入使用
`proposal_outcome_unknown`，恢复规则相同。任何一个错误码都不允许轮换
Request ID、重新应用动作或推进 Outbox。

最后一条仅适用于显式请求 `outcome-reporting-v1` 的 Session；客户端不能对
旧 Session 假设该语义。

持久身份保证适用于 Session mutation，不适用于进程内 Job 元数据。Proposal
Job 淘汰或重启后可以依据持久 Proposal 重建；Generation Job 不写事件日志，
Job 保留期结束或 Sidecar 重启后，相同请求可能再次执行。

Snapshot 响应包含 `identifier_history` 和 `identifier_history_hash`。History
会随 Session lineage 增长，也可能包含历史 Proposal/Arbitration 文本，因此
应按事件日志保护，并在保存时保留未知增量字段。所有 SDK 默认响应上限为
2 MiB，且只能在各自文档规定的范围内调整；大型 lineage Snapshot 传输仍受该
上限约束。

SDK 有意采用源码优先方式，尚未发布到 PyPI、npm、NuGet 或 Maven Central。
Vendor 时应固定本仓库 Revision。路由兼容性由
[`conformance/routes.json`](conformance/routes.json) 定义。

游戏专用示例位于 [`examples/mods`](../examples/mods)。它们展示宿主事件
如何进入 Rin，以及游戏在何处验证并应用 Proposal。它们是接入模板，不是
适用于每个游戏版本的通用补丁。

所有 SDK 的 Commit 生命周期、Outbox 和重试规则以
[`docs/outcome-reporting.zh-CN.md`](../docs/outcome-reporting.zh-CN.md) 为准。

SDK 源码按 [MIT License](../LICENSE) 发布。

# Rin SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 `rin.protocol/v2` HTTP 边界的轻量、源码优先客户端。

这些 SDK 消除传输样板代码，不会把游戏权威移动到客户端库。

[`hostkit`](hostkit) 下的类型化 Go 参考实现定义通用 Host 端口与可重启
Coordinator；所有权和生命周期契约见[通用 Host SDK 指南](../docs/host-sdk.zh-CN.md)。

SDK Workflow Helper 会校验接入声明的
[宿主持久 Profile](../docs/host-durability.zh-CN.md)。客户端库不能凭空
创造游戏没有提供的持久性或世界事务。

| 语言/Target | Runtime | SDK Profile | 异步建议 |
| --- | --- | --- | --- |
| Python | 3.9+ | `transport` | 实时游戏从 Worker 调用 |
| JavaScript | Node 18+ / 现代 Fetch 宿主 | `transport`、`streaming` | 基于 Promise |
| C# | .NET 6+ | `transport`、`streaming` | 基于 `Task` |
| C# 兼容构建 | .NET Standard 2.0 | `transport` | 基于 `Task` |
| Java | 17+ | `transport` | 基于 `CompletableFuture` |
| Lua | 5.1+ 宿主 | `transport` | 基于 Callback |

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
  Session State。对于 `rin.reducer-projection/v2` 之前的 Proposal，Rin 保留
  这些坐标和结构字段，但会通过玩家文本门禁升级 `summary`/`rationale`；
- `event_exists` 是其他请求造成的冲突，不是 duplicate 确认；
- Proposal 始终是建议；游戏接受或拒绝后回报类型化 Invocation、Run 与
  Outcome，Report 不是执行授权。
- 应把 Proposal 的 `summary` 与 `rationale` 用作玩家文案：Rin 由游戏编写的
  动作描述和固定 stance 模板生成它们。`policy_source`、
  `recalled_memory_ids`、`goal_id`、可选增量字段 `boundary_id` 与完整
  `proposed_goal` 是私有审计/集成元数据，绝不能直接展示给玩家。Action 的 ID、
  Kind、Target 与 Parameter 也是集成数据，除非游戏另行授权；
- 所有随附 SDK 都采用宽容 Object 解码。动态客户端会自然保留 `boundary_id`；
  Unity typed 示例已显式声明，旧 typed 客户端可以安全忽略这个 v1 增量响应字段。

收到 `mutation_outcome_unknown` 后，应保留非 Proposal Operation，并且只用
其完全相同的 typed payload 与 ID 重试；该 mutation 可能已经持久化，其他
Session mutation 会阻塞到确认完成。Proposal 写入使用
`proposal_outcome_unknown`，恢复规则相同。任何一个错误码都不允许轮换
Request ID、重新应用动作或推进 Outbox。

持久身份保证适用于 Session mutation，不适用于进程内 Job 元数据。Proposal
Job 淘汰或重启后可以依据持久 Proposal 重建；Generation Job 不写事件日志，
Job 保留期结束或 Sidecar 重启后，相同请求可能再次执行。

Snapshot 响应包含 `identifier_history` 和 `identifier_history_hash`。History
会随 Session lineage 增长，也可能包含历史 Proposal/Arbitration 文本，因此
应按事件日志保护，并在保存时保留未知增量字段。整个 Snapshot 都是可信、
不透明的状态：其中 SHA-256 canonical checksum 可发现意外损坏或未同步修改，
但不验证来源，也无法阻止能重算 checksum 的一方。

Restore 必须提供来自运行中游戏可信内容 manifest 的 `expected_binding`。它必须
与导入 Snapshot 的 Binding 一致；target Session 已存在时还必须与该 Session
的 Binding 一致。不得通过读取 Snapshot 来填写它。

完整 inline Snapshot 的 compact JSON 上限为 16 MiB。Rin 超限时返回
`413 snapshot_too_large`，绝不截断内容。所有 SDK 默认响应上限为 32 MiB，
与服务端默认 32 MiB 请求正文上限匹配，并为 envelope、Restore 元数据和持久
EventRecord framing 预留空间。Session Transfer 是大 lineage 的受支持路径。
JavaScript 与 .NET 6+ C# Target 已提供 streaming source/sink helper；Python、
Java、Lua 与 C# `.NET Standard 2.0` 兼容 Target 只实现 `transport` Profile，
不宣称支持大 lineage Transfer。

Live Session State 默认使用 16 MiB compact JSON 预算；造成超限的 Mutation 会在
持久化前返回 `413 state_too_large`。运维配置最高只能提高到 24 MiB，以保证响应
envelope 仍可读取。

SDK 有意采用源码优先方式，尚未发布到 PyPI、npm、NuGet 或 Maven Central。
Vendor 时应固定本仓库 Revision。路由兼容性由
[`api/openapi.json`](../api/openapi.json) 定义；
[`conformance/routes.json`](conformance/routes.json) 是它生成的覆盖清单。每个
operation 标记为 `transport` 或 `streaming`；所有 client 必须覆盖 transport
profile，只有提供有界 stream API 的 client 才能声明 streaming profile。
其中 `request_schema` 与 HTTP Decoder 的 Route Binding 同时生成，不再另行
维护一份 Schema Switch。

[`conformance/sidecar-corpus.json`](conformance/sidecar-corpus.json) 是共享的
真实传输 Corpus。`make test-sdk-sidecar` 会构建真实 Sidecar，只执行一次严格
Wire 用例，然后让 Python、JavaScript、C#、Java、Lua 对同一进程与请求模板执行
Health、首次 Mutation、精确重试和超时检查。Lua Runner 使用它通常由宿主持有的
HTTP/JSON Port，不会伪称 SDK 自带网络栈。

游戏专用示例位于 [`examples/mods`](../examples/mods)。它们展示宿主事件
如何进入 Rin，以及游戏在何处验证并应用 Proposal。它们是接入模板，不是
适用于每个游戏版本的通用补丁。

所有 SDK 的 Action Report 生命周期、Outbox 和重试规则以
[`docs/action-lifecycle.zh-CN.md`](../docs/action-lifecycle.zh-CN.md) 为准。

SDK 源码按 [MIT License](../LICENSE) 发布。

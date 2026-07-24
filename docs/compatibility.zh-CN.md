# Rin 0.6 兼容矩阵

[English](compatibility.md) | [简体中文](compatibility.zh-CN.md)

## 发布状态

Rin `0.6.0` 是 Preview、pre-1.0 软件。项目会记录兼容与迁移行为，但后续
pre-1.0 minor 版本可以进行不兼容变更，前提是在 Changelog 与迁移指南中明确说明。
接入方应固定到精确的 Rin 仓库 Revision 或已验证 Release Tag。

名为 0.1 至 0.5 的实施里程碑只是历史标签，不代表一定存在同名公共 Tag。

## 契约事实来源

| 关注点 | 权威来源 | 说明 |
| --- | --- | --- |
| HTTP 路径、方法、状态码、必填字段与 JSON Shape | [`api/openapi.json`](../api/openapi.json) | 0.6 唯一 Wire Schema 来源 |
| 事务、重试、权威与持久语义 | [Protocol v1](protocol-v1.zh-CN.md)和[Outcome 记账](outcome-reporting.zh-CN.md) | 叙述语义补充 OpenAPI |
| SDK 路由覆盖清单 | [`sdk/conformance/routes.json`](../sdk/conformance/routes.json) | 只用于覆盖核对，不能覆盖 OpenAPI |
| 发布变化 | [变更日志](../CHANGELOG.zh-CN.md) | 包括 Breaking 与安全相关变化 |
| 升级操作 | [v0.6 迁移](migration-v0.6.zh-CN.md) | 替换旧部署前必须阅读 |

若文字说明与 Wire Schema 在路径、字段是否必填、Shape 或 HTTP 状态上冲突，
以 `api/openapi.json` 为准，并把文字差异视为文档 Bug。

## 公共兼容矩阵

| 表面 | 0.6 契约 | Legacy/导入行为 | 使用方要求 |
| --- | --- | --- | --- |
| 分发版本 | `0.6.0` Preview | 更早编号里程碑可能没有 Tag | 固定一个 Commit 或已验证的 `v0.6.0` Tag |
| Wire 标识 | `rin.protocol/v1` | 既有 v1 Event 按文档所述 Legacy 语义继续 Replay | 每个 JSON 请求都发送精确标识 |
| 路由 | OpenAPI 描述的 18 个 Path、20 个 Operation | 不提供隐式别名路由 | 按 OpenAPI 生成或实现；Route Inventory 只核对覆盖 |
| 请求 Object | 封闭；未知成员会被拒绝 | 新增安全必填字段后，旧请求可能失效 | 不发送试探字段 |
| 响应 Object | 可增加字段 | 旧宽容 Client 可忽略未知字段 | 宽容解码；把每个 Snapshot 作为不透明 JSON 保存，并在 Restore 时回传原始 JSON，不经由会丢字段的 Typed Model 往返转换 |
| 整数 | JSON 精确范围 `-9007199254740991` 至 `9007199254740991`，许多字段还要求非负 | 超范围值会被拒绝，不会舍入 | 发送 JSON Number，不使用字符串整数或 JavaScript `BigInt` |
| 请求文本 | 原始 HTTP 正文必须在 JSON 解码前满足 UTF-8 | 非法原始字节返回 `400 invalid_json` | 使用 UTF-8 JSON 编码 |
| Commit Outcome | Commit 与每个 Batch Item 都必须显式携带 `accepted`，包括显式 `false` | 省略表示非法，不表示拒绝 | 使用能保留字段存在性的 DTO/Serializer |
| HTTP 错误 | 非 2xx，Envelope 为 `{ok:false,error:{code,message,field?}}` | — | 按有界 `error.code` 分支，不匹配 Message 文本 |
| Job 失败 | Query/Cancel 可返回 HTTP `200`，终态 Job 的 `data.error` 表示 Operation 失败 | Job 记录有界且只在进程内 | 分别判断 HTTP 成功与 Job 成功 |
| Session 语义 | Feature 固定在 Session State 中 | 未启用 Feature 的 Session 保留历史语义 | 通过 `/health` 协商，在创建时选择；绝不编辑 Snapshot 添加 Feature |
| Restore | `expected_binding` 必填，来源是运行中可信 Manifest | 不带 Identifier History 的 Legacy Snapshot 以永久不完整 Coverage 导入 | 保持全局唯一 ID，并遵循迁移指南 |
| Identifier 身份 | Request/Event ID 在完整 Lineage 内永久保留 | Legacy 不完整 History 无法恢复已淘汰 ID | 不轮换未决 ID，也不复用被放弃分支的 ID |
| Reducer 投影 | `rin.reducer-projection/v2` | v1 Checkpoint 作为派生缓存丢弃，Event Log 不改写 | 旧 Proposal 展示可在读取或 exact retry 时被重建 |
| Snapshot 传输 | Compact JSON 16 MiB；外层请求/响应默认 32 MiB | 超大 Legacy Event 可本地 Replay，但不能通过 Inline API | 为 Identifier History 规划容量；当前没有 Streaming |
| File Store | 仅支持具有可靠 `flock`、rename、sync 的本地 `darwin`/`linux` 文件系统 | 其他 GOOS Fail Closed | Windows、HA、远程或共享存储使用其他协调 Store |
| SDK 分发 | 源码优先：Python 3.9+、Node/Fetch、.NET 6+、Java 17+、Lua 5.1+ | 未发布到语言 Registry | Vendor 完整 Client 目录并固定 Rin Revision |

Snapshot 前向兼容是 Client Storage 保证，不是 Server Round-trip 保证。
0.6 Runtime 会接受 `Snapshot` 和 `SessionState` 中符合 OpenAPI 增量规则的
未知成员，但前提是 `state_hash` 仍与 0.6 所理解的 State 投影匹配。Runtime
会忽略这些未知成员，之后返回的 Snapshot 或 State Response 也不会重新输出
它们。若未来 Producer 把未知 State 成员纳入 `state_hash`，0.6 会以
`400 invalid_snapshot` Fail Closed，而不会恢复无法验证的投影。未知成员仍
计入完整 Inline Snapshot 的 16 MiB 上限。

## Feature 兼容

| Feature | 启用后的作用 | 未启用时 |
| --- | --- | --- |
| `outcome-reporting-v1` | 游戏先 Apply/Reject 并持久化 Outbox，再回报；延迟 Outcome 按发生时间合并 | 历史 Fresh-head Commit 与按到达顺序行为 |
| `memory-archive-v1` | 有界情节细节与确定性有损 Summary | 只保留最新的有界情节窗口 |
| `belief-conflicts-v1` | 有界、带来源的冲突 Claim 与 Selected 兼容投影 | 只有 Selected Belief 投影 |
| `goal-candidates-v1` | Policy 只能选择游戏提供的完整候选 Goal；Accepted Outcome 后采用 | Candidate Goal 被拒绝 |
| `actor-activity-v1` | 持久 Region 与 Awake/Dormant 状态 | Activity Endpoint 被拒绝 |
| `arbitration-v1` | World Revision、建议性多 Proposal 仲裁和原子 Batch Commit | Legacy 单 Proposal 协调 |

新接入通常应启用 `outcome-reporting-v1`。其他 Feature 只应在游戏已经实现并持久化
对应契约时启用。

## Provider 与 Generation 边界

Rin 会在解码前严格检查游戏到 Sidecar 的原始请求正文和成功 Provider JSON；
非法 UTF-8 与未配对 Unicode Surrogate 会被拒绝。解码后的 Generation Content
还要检查空值、NUL、字节上限和单个顶层 JSON Object。非 2xx Provider Body 只用于
有界错误分类，绝不会成为 Generation Content 或 Session State，也没有 Content
有效性保证。游戏仍须把成功解码的 Generation Content 当作不可信数据，并验证
自己的领域 Schema 与 Canon。

## Hash 能证明什么

Event Hash、Snapshot Checksum 和 Checkpoint Checksum 可发现不一致或意外损坏，
但它们无密钥，不是签名、MAC 或来源证明。能替换完整 History 的一方可以重新计算
有效事件链和派生 Artifact。必须使用外部访问控制保护数据目录和备份。

# 迁移到 Rin 0.6 Preview

[English](migration-v0.6.md) | [简体中文](migration-v0.6.zh-CN.md)

本文适用于从更早源码 Revision 切换到 Rin `0.6.0`。请先阅读
[兼容矩阵](compatibility.zh-CN.md)。[`api/openapi.json`](../api/openapi.json)
是 Wire Shape 的权威来源。

## 升级前

1. 停止所有可能写入该数据目录的 Sidecar。
2. 对完整本地数据目录及匹配的游戏存档做协调备份；不要直接复制正在使用的
   File Store。
3. 记录当前 Rin Commit、Client/Adapter 源码 Revision、Session Feature 和游戏
   Content Binding。
4. 尽可能排空游戏 Outcome Outbox；否则把所有未确认 Outbox 项和 Proposal
   Attempt 与对应存档一起持久化。
5. 同时升级 Sidecar 与所有 Vendor 的 Client/Adapter 文件，不要只复制一个 SDK
   源文件。

## 必须审查的 Wire 变化

### 安全整数

每个公共 JSON 整数都必须能在 `-9007199254740991` 至 `9007199254740991` 范围
内精确表示；字段自己的非负或更窄约束仍然有效。JavaScript 接入必须拒绝不安全的
`number`，不得舍入，也不得发送 `BigInt` 或字符串整数。

### Outcome 字段必须存在

每个 Commit Request 和 Batch Commit Item 都必须携带 `accepted`。动作实际生效
后发送 `true`，游戏拒绝后显式发送 `false`；缺失或 `null` 返回
`400 invalid_request`。

### UTF-8 与 JSON Shape

原始 HTTP 请求正文必须使用 UTF-8；非法原始字节或未配对 JSON Unicode
Surrogate 会在 JSON 解码前以 `400 invalid_json` 失败。请求 Object 封闭并拒绝
未知字段。响应 Object 可以增加字段，因此 Client Decoder 必须宽容。
Client 自己保存 Snapshot 时必须持久化原始、不透明 JSON，并在 Restore 时直接
回传，不能经由会丢字段的 Typed Model 往返转换。这不表示 Server 会保留未知
字段：0.6 会忽略 `Snapshot`/`SessionState` 中的未知增量成员，之后只输出自身
已知投影。只有 `state_hash` 与该投影匹配时 Restore 才会成功；若未来 Hash
纳入未知 State 成员，0.6 会以 `400 invalid_snapshot` Fail Closed。

成功 Provider JSON 也会在解码前严格检查非法 UTF-8 和未配对 Unicode Surrogate。
非 2xx Provider Body 只用于有界错误分类，绝不会成为 Generation Content 或
Session State。

### 错误层次

非 2xx HTTP 失败使用：

```json
{"ok":false,"error":{"code":"invalid_request","message":"...","field":"..."}}
```

成功查询 Job 也可能返回 HTTP `200`，但终态 Job 的 `data.error` 表示异步
Operation 失败。不能把每个 HTTP `200` Job 响应都当作 Proposal 或 Generation
成功。

## Session 行为

### 既有 Session

不得通过编辑 Snapshot 或 Event Log 添加 Feature。未启用
`outcome-reporting-v1` 的既有 Session 会有意保留历史 Fresh-head Commit 与
按到达顺序 Reducer 行为；Restore 不会静默切换到新语义。

游戏若需要“先 Apply、后记账”，应创建一个在 Create Request 中启用
`outcome-reporting-v1` 的新 Session Lineage，再通过游戏自身逻辑迁移权威世界
Fact。不得伪造或改写 Rin History。

### 新的先处理、后记账生命周期

提交 Proposal 前，先把完整 Propose Request、Operation 身份以及稍后取得的 Job
ID 持久化为 Proposal Attempt。Submit、Poll、Timeout、Cancel 都可能结果未决。
此时必须恢复同一身份并阻塞新 Turn；只有确认不存在在线 Proposal 后才能执行
Offline Fallback。

收到 Proposal 后：

1. 重新读取权威游戏状态并本地校验 Action。
2. 在游戏权威线程 Apply 或 Reject。
3. 在同一个游戏事务内持久化 Applied Marker 和完整 Outcome Outbox 项。
4. 从 Outbox 报告完全一致的 Commit。
5. Timeout 或 `mutation_outcome_unknown` 时，用相同 Typed Payload 和 ID 重试，
   不得再次执行 Action。

在线 Proposal Operation 内部的 Provider 失败可以由 Rin 的 Deterministic Policy
接管；Sidecar 投递结果未决是另一种状态，必须 Fail Closed。

## Snapshot 与 Restore 迁移

0.6 的每个 Restore Request 都要求 `expected_binding` 来自运行中游戏的可信
Content Manifest，绝不能从导入 Snapshot 复制。它必须同时匹配 Snapshot Binding
和任何已存在目标 Session 的 Binding。

新 Snapshot 包含 `identifier_history` 与 `identifier_history_hash`。History 会
永久保留已经接受的 Request/Event ID。缺少这些字段的 Legacy Snapshot 仍可按
`coverage_complete=false` 导入；导出前已淘汰的 ID 无法重建，因此该 Lineage
必须永远继续使用全局唯一 ID。

完整 Compact Snapshot 必须处于 16 MiB 内。Rin 不会为了通过上限而截断
Identifier History，当前也没有 Streaming Snapshot Transport。

Snapshot 是可信、不透明状态；Hash 只能发现意外损坏，不能证明来源，也不能阻止
能编辑并重算 Hash 的一方。

## Projection 与存储迁移

`rin.reducer-projection/v2` 会改变派生展示和有界 Summary 取样。旧 Proposal 的
exact retry 可返回重建后的 `summary`/`rationale`，但原始 Action、审计 ID、
Revision 和 Head 保持不变。原始事件字节不会改写；旧私密字符串仍可能存在于
Event Log、内嵌 Restore Payload 和备份中。

内部 v1 Checkpoint 是已经过期的派生 Cache。Rin 会回退到其他兼容 Checkpoint
或 Genesis Replay，不需要人工转换。普通 Lazy Load 不是不依赖 Checkpoint 的
完整审计；运维需要对每个 Session 执行 Genesis-to-head 审计时，应调用
`Engine.VerifyAll()`。

随附 File Store 只支持具有可靠锁、原子 Rename 与 Sync 语义的本地 `darwin` 和
`linux` 文件系统。不得迁移到 Windows、NFS、SMB、FUSE 或云同步目录。

## 验证清单

- 游戏与 Sidecar 对 Binding 和启用 Feature 的理解一致。
- 所有请求整数都处于安全范围。
- 已测试 `accepted=true` 与显式 `false`；省略会失败。
- 未知请求字段失败，而增量响应字段不会破坏 Client。
- HTTP 错误与 HTTP-200 终态 Job 错误分别处理。
- Proposal Attempt 与 Outcome Outbox 能在进程丢失后恢复。
- Sidecar Timeout 不能让 Fallback 越过未决 Proposal。
- Restore 从运行中 Manifest 取得 `expected_binding`。
- 使用真实保存数据 Fixture 覆盖 Legacy Snapshot 导入和 exact retry。
- 发布指南中的完整仓库与 SDK 测试命令通过。

## 回滚

不要让旧 Binary 指向已经由 0.6 打开的唯一数据副本。停止 Sidecar 并保留升级后
目录，只在升级前备份的副本上使用完全匹配的旧 Client 与游戏 Build 测试回滚。
不得编辑 JSONL、Snapshot Hash、Identifier History 或已发布 Tag 来强制回滚。

# Session 生命周期与数据治理

[English](session-lifecycle.md) | [简体中文](session-lifecycle.zh-CN.md)

状态：0.6 Preview 线已接受设计。OpenAPI 契约和实现必须遵循本文。

## 状态

Session 只有一条不可逆生命周期：

```text
active -> archived -> deleted tombstone
```

Active Session 接受普通 Mutation。Archive 是持久、幂等的转换，会冻结当前事件链
Anchor。Archived Session 只读：State、Timeline、Replay、Snapshot、Stats 与
Session Transfer Export 仍可用；Observe、Propose、Commit、Activity、
Arbitration、Restore 与 Import 返回 `409 session_archived`。

Archive 不等于备份。若删除后仍需恢复，Operator 必须先导出完整 Session
Transfer，校验终止 `complete` Frame 与 `stream_sha256`，并保存到独立存储。

只有 Archived Session 可以删除。删除会原子移除全部权威事件与派生 Artifact，
然后留下最小 Tombstone。Session ID 被永久退役，不能再次 Create 或 Import。

## 经过鉴权的操作

所有生命周期 Route 使用服务端已有 Bearer 鉴权。未配置 Token 的服务只适合可信
Loopback 边界。

`POST /v1/session/stats` 接受 `protocol_version` 与 `session_id`，返回生命周期
状态、Revision/Head、Event 数量、Event Log 精确字节数、Artifact 字节数、总受管
字节数、Soft/Hard Limit 及是否越界。Stats 不应为了计数字节而加载玩家内容。
损坏或不可读 Session 必须 Fail Closed，绝不能报告为不存在。

`POST /v1/session/archive` 要求稳定 `request_id`、`session_id`、完整可信 Expected
Binding，以及精确 `expected_revision` 与 `expected_head_hash`。持有 Mutation
Gate 时，所有前置条件必须匹配同一个已加载、已验证 Session。Store 持久化包含
Request 身份、冻结 Anchor、时间与 Canonical Request Digest 的 Archive Receipt。
Exact Retry 返回同一 Receipt 且 `duplicate=true`；修改后的 Request 返回
`request_id_conflict`。

`POST /v1/session/delete` 要求稳定 `request_id`、`session_id`、相同可信 Expected
Binding、Archive Receipt ID、冻结 Revision/Head，以及与完整 Session ID 完全相等
的 `confirmation`。Confirmation 不是鉴权；它用于降低误删或目标错位风险。
不支持 Wildcard、Prefix、空 ID 或批量删除。

## 持久删除

File Store 删除使用每 Session Event 与 Artifact Lock。它先把最小 Tombstone
作为 Fail-closed 删除意图持久发布，再把 Session Directory 改名为内部 deleting
名称，同步 `sessions` Directory，删除已改名 Directory 并再次同步父目录。启动时
会根据 Tombstone 完成被中断的删除，之后才能暴露 Store。Windows 使用
Write-through Rename；受支持 POSIX 平台使用 Rename 加 Directory Sync。

Tombstone 不包含 Event、Snapshot、生成文本、Actor、Fact、Goal 或 Binding 值，
只保留：

- Protocol/Tombstone Format Version；
- Session ID；
- Delete Request ID 与 Canonical Request Digest；
- 删除时间；
- 最终 Revision 与 Head Hash；
- Binding 的 SHA-256 Digest；
- Archive Receipt ID（该 ID 由 Archive Request Digest 派生）。

这些最小数据用于使删除重试确定，并阻止陈旧客户端复用已退役 Lineage 身份。
如果这些 Identifier 也属于个人数据，Tombstone 需要单独的保留策略。

## 容量策略

默认仍不限制，以保持兼容。Operator 可以配置每 Session Soft/Hard 受管字节上限：

- 超过 Soft Limit 的操作成功，但 Stats/Readiness Metric 会暴露；
- 若 Mutation 的保守编码预留会超过 Hard Limit，则在 Append 前返回
  `507 session_quota_exceeded`；
- 已持久 Request 的 Exact Retry 仍可读取和对账；
- 超限时 Stats、Export、Archive 与 Delete 仍可用；
- 可以跳过派生 Artifact，但绝不能截断 Event History。

Limit 覆盖 Event Log 及 Store 管理的 Snapshot、Checkpoint、Index、Archive
Marker 和 Uncertainty Metadata。Transfer Staging 有自身限制，在原子发布前不计入
Existing Session。

## 保留与隐私边界

Rin 删除无法擦除 Data Directory 之外的副本：游戏存档、Session Transfer
Export、文件系统或 Volume Snapshot、云备份、Replica、Embedding Application
日志和模型供应商系统都需要自己的策略。声明擦除前必须使这些副本过期。

从保留链中间删除一个 Event 会使所有后续 Hash、Request Receipt、Snapshot
Checksum 与 Transfer Anchor 失效。因此 Rin 支持完整 Session 退役，不支持
选择性原地 Event Redaction。产品需要选择性擦除时，不应把敏感自由文本写入 Rin。

备份必须在一致持有 Data-directory Lease 时捕获，或使用已验证 Session Transfer
Export。恢复原始文件系统备份也可能恢复 Archived Session 与 Tombstone；
对外服务前必须与外部删除 Ledger 对账。

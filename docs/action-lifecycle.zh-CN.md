# Host 动作生命周期

[English](action-lifecycle.md) | [简体中文]

本文定义 Rin `0.7.0` **Preview**、`rin.protocol/v2` 的执行与恢复契约。

## 各职责只有一个 Owner

| 职责 | Owner |
| --- | --- |
| 观察权威世界状态 | 游戏 Host |
| 定义 Capability 与完整绑定的 Offer | 游戏 Host |
| 选择一个已提供动作 | Rin Policy |
| 接受、拒绝、启动、取消与执行 | 游戏 Host |
| 持久化 Operation Marker 与 Report Outbox | 游戏 Host |
| 记录 Memory、Fact、Goal 与审计历史 | Rin |

模型不会获得通用命令执行器。`ActionOffer` 已包含 Capability Version、
Descriptor Digest、Argument、Target、Epoch、Observation Sequence 与 Deadline。
选择 Offer 不会授权任何其他输入。

## 生命周期对象

`ActionInvocation` 把所选 Offer 绑定到稳定 `operation_id`；`ActionRun` 回报单调
进度；`ActionOutcome` 记录 Host 观察到的终态效果。

```text
offered → rejected
       └→ accepted → queued → running → succeeded
                              ├────────→ failed
                              ├────────→ cancelled
                              ├────────→ interrupted
                              ├────────→ stale
                              └────────→ outcome-unknown
```

终态 Run 必须携带 Operation ID、终态 Status 与 Expected Epoch 都匹配的 Outcome；
非终态 Run 不得携带 Outcome。Fact 与 Goal Update 只能出现在终态 Accepted Report。

## 持久 Pending Turn

第一次 Proposal Request 前应保存：

- 稳定 Operation ID；
- 完整 `ProposeRequest`，包括 Decision Window 与 Offer；
- 必须先于 Proposal 的 Observation；
- Submit 后、第一次 Poll 前保存 Job ID。

重启后重试确切请求。如果进程内 Job 已消失，重新提交同一请求。创建新的 Request
ID 会形成第二次逻辑决策，不属于恢复。

## 应用游戏效果

选择并记录一个 `HostDurability` Profile：

- `advisory`：Adapter 无法证明世界修改具备崩溃安全；
- `idempotent-action`：`Execute(operation_id)` 与游戏存档可阻止第二次效果；
- `transactional-action`：游戏效果、Operation Marker 与确切 Report Outbox
  Entry 原子提交。

Idempotent Host 应：

1. 验证 Proposal 身份、所选 Offer、Epoch、Observation Sequence、Capability
   Digest、Target 有效性与 Deadline；
2. 用稳定 Operation ID 调用游戏所有的 Executor；
3. 持久化 Marker 与确切 Report；
4. 只能按 Operation ID 重试。

Transactional Host 必须让第 2、3 步处于同一游戏事务。仅仅在游戏 API 调用附近
Flush 一个 JSON 文件，不能宣称 Transactional Durability。

## Outcome Outbox

Rin Report 是幂等的，但 Host 必须保留确切请求，直到收到成功响应。同一权威
Scope 开始下一 Turn 前应先 Drain Outbox。

Endpoint 失败时不得把终态 Report 转换成 Observation；这会丢失 Proposal、
Invocation 与 Operation 身份。网络结果不明表示“重试此 Report”，不是“编造
另一条 Event”。

已经确认的 Report 不得使游戏效果再次运行。Event Replay 只重建 Rin 状态。

## Rejection 与失败

Rejection 只能包含 Proposal ID、Event ID、Decision、Summary 与可选 Tag，
不能包含 Invocation、Run、Outcome、Fact 或 Goal Update。

已经接受、但未产生预期效果的 Operation 不是 Rejection：应回报 Accepted
Invocation，并使用终态 `failed`、`interrupted`、`stale` 或
`outcome-unknown` Run 与匹配 Outcome。`outcome-unknown` 是需要后续对账的
持久状态，不是运行另一个动作的许可。

## 并发决策

对于 `simultaneous` Decision Window：

1. 收集基于同一权威 Window 的 Proposal；
2. 对共享 Target 做确定性 Arbitration；
3. 按 Host 并发规则应用已接受 Operation；
4. 通过 `BatchActionReportRequest` 原子回报结果。

Batch 中每条 Report 保留自己的 Operation 生命周期；外层 Tick 是该 Batch
共享的权威发生 Tick。

## 审查清单

- Request、Event、Proposal、Offer、Window、Operation ID 各有不同且稳定的语义。
- 不用渲染帧或墙钟时间替代 Host Step/Event Clock。
- 保存的 Pending Turn 足以重新发送语义完全相同的请求。
- 所选 Offer 与持久化的 Host-authored Offer 比较。
- Execute 前立即检查 Epoch 与 Deadline。
- Executor 不接受模型生成的方法名或 Argument。
- Applied Marker 与 Outbox Entry 有明确容量上限。
- 测试 Restart、响应丢失、状态损坏、Outbox 满与磁盘写入失败。

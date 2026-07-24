# 动作结果记账

[English](outcome-reporting.md) | [简体中文](outcome-reporting.zh-CN.md)

本文定义 `rin.protocol/v1` 的 Proposal、游戏应用与 Commit 事务语义。新 Session
必须在 `CreateSessionRequest.features` 中加入 `outcome-reporting-v1` 才会启用；
未启用的 Session 保持历史上的 head 新鲜度检查、逐步截断和按到达顺序合并，
从而让旧事件日志继续得到相同的重放结果。

## 唯一权威

游戏引擎是世界事实的唯一权威。Rin 的 Proposal 是应用前建议，Commit 是游戏
处理 Proposal 后的结果记账：

```text
Rin 产生 Proposal
→ 游戏在权威线程重新验证动作、目标和前置条件
→ 游戏应用动作，或者决定拒绝
→ 游戏把结果加入自己的持久 Outcome Outbox
→ 游戏用 Commit 向 Rin 回报 accepted/rejected 结果
```

Rin 不会通过 Commit 执行游戏动作，Commit 成功也不应触发游戏再次执行动作。

为保持 v1 线格式兼容，路径 `/v1/action/commit`、类型名
`CommitRequest`、字段 `accepted` 和适配器本地字段 `committable` 保持不变。
它们表示结果记账能力，不表示 Rin 授权或执行动作。

## 字段语义

- `accepted=true`：游戏确认 Proposal 对应的动作已经实际生效并成为 Canon。
- `accepted=false`：游戏确认没有产生该动作的世界效果。拒绝原因可以放入
  `outcome` 供审计；由失败产生的新观察应另行调用 `observe`。拒绝结果不得
  携带 `facts` 或 `goal_updates`。
- `status=pending`：Rin 尚未收到并结算游戏结果，不表示动作等待 Rin 激活。
- `committable=true`：Proposal ID 可向当前 Sidecar 回报，不是执行授权，
  也不替代游戏在应用前的本地新鲜度检查。
- `tick`：动作实际发生或被拒绝的游戏 tick。它不得早于 Proposal tick，
  但可以早于结果到达时的当前 Session tick。

游戏必须在应用前重新读取 Session state，并检查自己的权威前置条件。启用
`arbitration-v1` 时，应要求
`state.world_revision == proposal.based_on_world_revision`（或先仲裁整组
Proposal）；未启用仲裁时，应要求保留的 Proposal 仍为 `pending`，且
`state.revision == proposal.created_revision`。`based_on_revision` 与
`based_on_head_hash` 指向 Proposal 事件之前的状态，只用于审计，不能直接与
Proposal 写入后的 Session head 比较。若新鲜度或游戏前置条件失效，游戏不得
应用动作，并可回报 `accepted=false`。

Proposal Job 超时不等于 Proposal 不存在。应使用相同 request ID/job ID
重试提交或查询，并消费 DELETE 的最终响应：取消可能输给已经持久化的
Proposal。在投递或取消尚未确认时必须 fail closed，不得执行离线 fallback。
只有接入层确定没有创建在线 Proposal（例如 Sidecar 已禁用，或初始连接被明确
拒绝）时，fallback 才安全。

## 延迟结果

游戏应用动作后，Observation、其他角色结果或网络延迟可能已经推进 Rin 状态。
这种结果是延迟到达的权威事实，不是错误。Commit 不会只因当前
Revision、World Revision 或 Session tick 已前进而拒绝它。

Rin 按游戏中的发生 tick 合并已接受的延迟结果：

- 调度时间只会前进，不会倒退；
- 已接受动作与情节记忆保持按发生时间排序；
- Fact 由服务端写入 `observed_tick`，同一 subject/predicate 的旧报告不会
  覆盖更新的事实；
- Goal 由服务端写入 `updated_tick`，并用 `progress_accumulator` 保留未截断
  的累计值，使正负 progress delta 保持可交换；较旧的显式 status 不能覆盖
  较新的 status；`status_explicit` 用于区分游戏显式状态和由进度自动投影的
  active/completed，`status_updated_tick` 与 `status_source_event_id` 则让
  显式状态独立于纯进度更新排序（同 tick 用事件 ID 决定）；
- 已解决 Proposal 会保存 `outcome_event_id` 与 `outcome_tick`，拒绝结果也
  一样，因此在 Proposal 保留期间可以审计其事件 ID。

这些字段是响应/状态元数据，请求 DTO 不得主动设置（保持省略或零值）。
调用方通过外层 Observe 或 Commit 的 `tick` 提供发生时间，Rin 据此派生
这些元数据。

Proposal 生成期间的 `state_changed` 和应用前 Arbitration 的
`proposal_stale` 仍会拒绝旧建议；它们不能用于拒绝游戏已经处理的结果。

## 批量结果

`/v1/action/commit-batch` 必须启用 `arbitration-v1`，并原子记录一组结果；
本文的“先处理、后记账”和延迟 Outcome 语义还要求启用
`outcome-reporting-v1`。所有 Item 必须来自同一个原始
`based_on_world_revision`，但该版本可以早于报告到达时 Rin 的当前版本。所有
Item 还会共享外层 `BatchCommitRequest.tick` 作为实际发生 tick；发生时间不同
时必须按 tick 分组，或者分别调用 Commit。混合不同原始版本会以
`proposal_base_mismatch` 拒绝整个请求且不产生部分修改。

## Outbox 与重试

异步提交 Proposal 前，游戏还应持久化一条独立的 Proposal Attempt，保存完整
Request、游戏 Operation 身份与可选 Job ID。`proposal_outcome_unknown` 必须
保留这条 Attempt 并阻止新 Turn；携带该错误码的终态 Job 仍然属于未决状态。
游戏应持续恢复完全相同的 request/job 身份，直到 Rin 返回 Proposal 或确认终态
中不存在 Proposal。成功拿到 Proposal 后，也只能在下述同一个权威事务中移除
Attempt，从而关闭“收到 Proposal 到持久化结果报告”之间的崩溃窗口。

游戏应在同一个权威事务中应用动作并持久化 Outcome Outbox 项。Outbox 至少保存：

- 在整个 Session lineage 内稳定且唯一的 Commit `request_id` 和 `event_id`；
- `proposal_id`、发生 tick、accepted、outcome；
- 回报所需的 tags、facts 和 goal updates。

一个 accepted 回报对每个 Goal 最多包含一条 update，避免发生时间合并受数组
顺序影响。

网络超时或暂时错误时，游戏只使用同一 `request_id` 重报完全相同的 typed
payload，不得重新应用动作。Rin 会把该 ID 绑定到完整请求的 canonical digest；
改变 Event ID、tick、accepted、outcome、tag、Fact、Goal update 或任何其他
typed 字段都会返回 `request_id_conflict`，不会产生第二种解释。

`mutation_outcome_unknown` 表示 Commit 可能已经持久化，但 Rin 无法完成确认。
游戏必须保留 Outbox 项、绝不重新应用动作，并且只重试这条完全相同的 Commit；
对账完成前，其他 Session mutation 会被有意阻塞。Proposal 生成继续使用兼容
错误码 `proposal_outcome_unknown`，并要求同样的 exact-attempt 恢复。

明确 duplicate success 会带首次 Commit 的 revision/head 和
`duplicate=true`；这些字段是不可变操作回执，不是 Rin 当前 State head。收到
普通成功或这种明确 duplicate 后才能删除 Outbox 项。来自其他请求的
`event_exists` 是冲突，不是该 Outbox 项已经记账的证明，不能据此确认或删除。

创建游戏存档前应先排空 Outbox，或者把未确认项、Proposal Attempt 与匹配的
Rin Snapshot 一起保存。Snapshot Identifier History 会跨重启和 Restore 永久
携带已接受的 Request/Event ID。Restore 会保留 pending Proposal，既让尚未
处理的存档 Attempt 能恢复并重新校验，也让已经处理的 Operation 通过存档
Outbox 完整补报 Fact、Goal update、近期动作和调度影响。恢复出的 Proposal
绝不授权执行动作；游戏必须用持久化 Attempt 与 applied-operation marker
区分尚未处理的动作和绝不能重做的动作。

恢复该存档时，必填 `expected_binding` 必须来自运行中游戏的可信内容
manifest，而不是 Snapshot。它必须与 Snapshot Binding 以及任何 existing
target Session 的 Binding 一致。Snapshot 的 SHA-256 canonical checksum 只能
发现意外损坏，不能认证来源或阻止能重算 checksum 的一方，因此 Snapshot 仍是
按事件日志保护的可信、不透明状态。完整 inline Snapshot compact JSON 上限为
16 MiB，服务端请求正文与随附客户端响应默认上限为 32 MiB。
`413 snapshot_too_large` 绝不截断已存 lineage。当前不提供流式 Snapshot
传输。

若 Sidecar Session 无法恢复、因而确实不存在匹配 Proposal，`observe` 只是降级
对账路径：它能按原始发生 tick 恢复权威事件的记忆和 Fact，但不能重建
Proposal 专属的 Goal delta、近期动作或调度。此时应把最终的绝对世界状态表达
为 Fact，不得宣称已经完成等价 Commit 对账，也不得为了获得新 Proposal 而重做
动作。`offline.*` Proposal 始终不能 Commit；Sidecar 恢复后通过 `observe`
报告实际 fallback 事件。

## 兼容迁移

旧接入若采用“先 Commit、成功后再 Apply”，应为新 Session 显式启用
`outcome-reporting-v1`，再迁移为“游戏先验证并 Apply/Reject，随后 Commit”。
请求字段与 HTTP 路径保持线格式兼容；该 Feature 会有意改变 reducer 语义和
持久状态元数据，因此绝不会自动加到已有 Session。未启用 Feature 的旧 Session
与事件日志继续保持历史重放结果。启用后的 Proposal、Fact 和 Goal 状态可能带有
上述可选发生时间元数据；Feature 启用前的 Snapshot 按旧语义继续可读。

Identifier History 是与 `outcome-reporting-v1` 无关的线格式增量。不带 History
的旧 Snapshot 仍可读取，但 coverage 会永久标记为不完整，因为导出前已淘汰的
ID 无法恢复。导入这类存档的游戏必须继续使用全局唯一 Request/Event ID。
Restore 会合并当前分支与导入 Snapshot 的 History，因此回滚不会允许重新使用
被放弃 future 中的 ID。

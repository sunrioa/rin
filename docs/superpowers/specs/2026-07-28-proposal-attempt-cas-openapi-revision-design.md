# Proposal Attempt CAS 与 MutationResult 契约修复设计

日期：2026-07-28
基线：`b1ce8cb0d4c9646f49045fa3d0dc9965b1aaa183`

## 目标

本次只修两个已确认问题：

1. Terminal Story 的延迟 Proposal 响应可能写回已经结算并清除的 Attempt，导致同一动作再次执行。
2. OpenAPI 允许 `MutationResult.revision` 为 `0`，但 Runtime、SDK 和现有说明要求它大于 `0`。

修复不增加运行时依赖，不调整无关协议，也不重构现有工作流结构。

## P1：Proposal Attempt 磁盘 CAS

### 问题来源

`ProposalAttemptCoordinator.resume()` 提交 Proposal Job 后，调用
`saveProposalAttempt(attempt)` 保存 `job_id`。Terminal Story 的实现会在取得文件锁并重读磁盘后，无条件覆盖 `next.attempt`。

如果另一个进程已经完成结算并清除 Attempt，较慢进程仍会把旧 Attempt 写回。后续流程会再次执行同一个权威动作。

### 接口调整

持久化接口改为：

```ts
saveProposalAttempt(
  expected: ProposalAttempt,
  replacement: ProposalAttempt,
): Promise<boolean>;
```

Store 必须在自己的事务或文件锁内比较完整 `expected`。只有当前值完全一致时，才能写入 `replacement` 并返回 `true`。值不存在或任何字段不同都返回 `false`，且不得写盘。

Coordinator 在网络提交前保留原 Attempt。收到 `job_id` 后用原值作为 `expected`，带 `job_id` 的值作为 `replacement`。CAS 失败时抛出 `proposal_attempt_changed`，不再等待 Job，也不结算该响应。

### 结算约束

Terminal Story 的 `settleProposalAttempt` 比较完整 Attempt，不再只比较 `operation_id`。同一个 `operation_id` 下的 Request、Session 或 Job 发生变化时，结算必须失败。

`pending_turn` 和 Store 中绑定的 `session_id` 也要与 Attempt 对应。检查放在现有锁内完成，避免内存状态与磁盘状态之间再次出现窗口。

### 回归测试

测试使用两个指向同一保存文件的 `StoryWorkflowStore`：

1. 慢进程读取尚无 `job_id` 的 Attempt，并停在网络响应处。
2. 快进程保存 Job、结算动作并清除 Attempt。
3. 慢进程继续处理旧响应，CAS 必须失败。
4. 最终保存中只出现一次动作、一个 Outbox 条目，Attempt 和 Pending Turn 均为空。

JavaScript SDK 另加一个小测试，确认 CAS 失败后不会调用 `waitForProposal`。

## P3：MutationResult revision 下限

在 OpenAPI 中新增 `JSONSafePositiveInteger`：

```json
{
  "type": "integer",
  "minimum": 1,
  "maximum": 9007199254740991
}
```

只有 `MutationResult.revision` 改用该 Schema。确实允许 `0` 的 Revision、Sequence 和计数字段继续引用 `JSONSafeUnsignedInteger`。

Contract 测试直接检查 `MutationResult.revision` 的引用和新 Schema 的上下限。生成器输出随后用仓库现有检查命令验证，不手工修改生成投影。

## 冗余代码处理

调用关系检查没有发现可以安全删除的整块工作流实现：

- `ProposalAttemptCoordinator` 同时被 Terminal Story、`WorkflowCoordinator` 和公开类型声明使用。
- `completeProposalAttempt` 是非事务型 Durability Profile 的完成路径，不能因为 Terminal Story 使用事务路径而删除。
- `WorkflowCoordinator` 是 SDK 的公开组合接口，不属于本次缺陷遗留代码。

本次会删除的只有被 CAS 取代的单参数 `saveProposalAttempt` 契约、无条件覆盖实现和相应测试桩。其他代码只有在定向测试或调用关系证明其不可达时才删除。

## 文档

根目录 `README.md` 和 `README.en.md` 会重新整理，但不改产品事实。改写遵循以下规则：

- 开头直接说明 Rin 是什么、当前开发状态和适用边界。
- 安装、运行和验证命令保持可复制。
- 删除宣传式形容词、重复结论和没有来源的能力描述。
- 中英文结构一致，但不做逐句直译。
- 不把本次内部竞态修复写成面向用户的卖点。

## 验证范围

只运行与改动相邻的检查：

1. JavaScript SDK 测试。
2. Terminal Story 测试。
3. OpenAPI Contract 定向测试和生成器检查。
4. `git diff --check`。

如果定向测试暴露跨包回归，再扩大到对应包；不会先运行全仓 Race、五语言 Corpus 或真实宿主测试。

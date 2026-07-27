# 通用 Host SDK

[English](host-sdk.md) | [简体中文](host-sdk.zh-CN.md)

`sdk/hostkit` 是 Rin 与权威游戏之间边界的可执行 Go 参考实现。它编排 Protocol
DTO，不实现游戏引擎、导航、物理、存档系统、模型 Provider 或任意命令执行器。

## 端口

| 端口 | 所有权与用途 |
| --- | --- |
| `RinTransport` | 提交/轮询 Proposal Job，并回报精确动作生命周期。 |
| `AuthorityDispatcher` | 把最终授权与执行编组到游戏所属线程。 |
| `HostStateStore` | 持久化带 Revision 的 Pending Decision、ActionRun 与 Outbox。 |
| `IdentityProvider` | 读取稳定 Session/Epoch/时间/Principal 身份，生成重启后仍不复用的 ID。 |
| `ObservationMapper` | 把有界引擎事件转换为不可变 Observation DTO。 |
| `CapabilityRegistry` | 解析、绑定并重复 TOCTOU 校验精确能力。 |
| `ActionExecutor` | 在游戏内执行或取消一个已授权 Invocation。 |
| `ArtifactPresenter` | 展示不可变外部 Artifact，但不授予世界权威。 |

游戏对象、线程、Socket、Future、Provider Token、模型输出和无界二进制数据不得
进入 `WorkflowState`。

## Coordinator 生命周期

1. `BeginDecision` 校验由游戏编写的 Request，只从 `IdentityProvider` 取得
   Operation ID，并在任何网络调用前提交 Pending Decision。已有 Pending
   Decision、未清空 Outcome Outbox 或 Action/Outbox 容量耗尽时拒绝新请求。
2. `ResumePendingWork` 先清空精确 Report。没有 Job ID 时才使用保留的 Request
   Identity 提交，随后保存 Job ID，并只做一次有界 Poll，不在内部 Wait Loop。Submit 成功但保存前
   崩溃时，依靠 Rin 幂等 Request Identity 恢复。
3. `DispatchAndEnqueue` 验证 Proposal 精确选择了 Pending Decision 中的一项
   Offer；在任何游戏效果之前预检 Retained State 容量与 Report Metadata。
   随后在所属线程绑定当前 Descriptor Digest 与 Epoch，检查可信 Principal 的
   Granted Scopes，通过 `ActionExecutor` 执行，按 Sealed Capability Output
   Schema 校验结构化 Output，并把精确 Accepted Action Report 提交到 Outbox。
4. `RecordTransitionAndEnqueue` 只接受单调的 queued/running/terminal 转换；
   超过 Invocation Deadline 的成功 Outcome 会被拒绝。
5. `ReconcileEpoch` 删除尚未提交的陈旧 Decision，并取消陈旧的活动动作。如果
   Capability 不支持取消或已被动态移除，结果变为 `outcome-unknown`，框架不会
   虚构已成功回滚。
6. `DrainOutbox` 只有在 Rin 确认精确 `ReportActionRequest` 后才删除条目。
   Transport 失败会保留内容等价的 DTO 与稳定 ID 供重试；Terminal Action 的
   最后一条 Report 被确认后，HostKit 同时删除其完整 Action Record。

`WorkflowState` v2 只保留 Active Action 与仍有未确认 Report 的 Terminal
Action；Actions 和 Outbox 上限均为 1024。因而一万个已确认 Immediate Action
不会形成不断增长的 Ledger。Idempotent/Transactional Host 的 Applied Operation
Marker 属于权威游戏 Store，HostKit 不再复制一份无界摘要。
`IdentityProvider.NewID` 在同一 Session 内永不复用，即使进程已经重启。

若执行已经开始，但 Executor 返回错误、非法 Lifecycle Data 或不符合 Schema 的
Output，HostKit 会先提交可恢复的 `outcome-unknown` Action 与精确 Outbox
Report，再返回 `ErrExecutionOutcomeUnknown`；它不会重试世界效果。

`HostStateStore.CommitEffect` 是持久保证边界。`transactional-action` Host
必须原子发布游戏效果与回调返回的 `WorkflowState`；`idempotent-action` Host
可以先按稳定 Operation ID 应用再保存；`advisory` Host 可以 Best Effort，
但不得声称前两种更强 Profile。

## 长时间移动

移动是由游戏实现的 Capability，不是通用坐标命令。Unity Host 可以映射到
NavMesh，Unreal Host 可以映射到 Gameplay Ability 或 Behavior Tree Task，
服务器 Mod 可以映射到原生寻路器。`ActionExecutor.Execute` 返回 `queued`
或 `running`，引擎回调随后调用 `RecordTransitionAndEnqueue`。Scene、World
或 Timeline 切换时调用 `ReconcileEpoch`，阻止迟到回调修改替换后的世界。

## 跨语言状态

Go 包是规范的类型化端口与状态机参考。无依赖
[C99 参考](../examples/native-host)在 GCC/Clang 与 MSVC 上应用相同的
Epoch、Descriptor、Operation ID 和 ActionRun 规则。
JavaScript、C#、Java、Lua、Godot、
Unity、Fabric、BepInEx 与 Luanti 已提供 Protocol v2 Pending Turn 和精确
Outbox Workflow；它们的引擎侧接口应采用相同八个边界，语言语法差异不能改变
所有权、Epoch、重试或 ActionRun 语义。

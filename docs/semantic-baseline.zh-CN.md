# Session 语义基线

[简体中文](semantic-baseline.zh-CN.md) | [English](semantic-baseline.md)

Rin Protocol v1 对每个新建 Session 只有一个安全基线：
`outcome-reporting-v1`。新的 Create Request 若省略它，会在 `features` 字段返回
`400 invalid_request`。这消除了“游戏权威的 Apply-then-report Transaction”与
“Legacy Fresh-head Commit”之间的静默选择。

`GET /health` 同时返回：

- `features`：当前 Runtime 理解的全部 Capability；
- `recommended_features`：新 Session 的强制基线。

JavaScript/TypeScript 与 C# SDK 将其暴露为 `safeBaseline`；原
`authoritative` 名称作为源码兼容 Alias 保留。Capability Negotiation 默认使用
Safe Baseline。

## 兼容边界

Event Log 仍然权威。缺少 `outcome-reporting-v1` 的旧 v1 History 继续按原
Reducer 行为 Replay；原 Create Request 的 Exact Retry 仍返回已持久结果。
Session Transfer 可以迁移同一 Legacy Lineage，但不会改变它。

基线只在建立新 Lineage 时强制：

- Fresh Create 必须包含 `outcome-reporting-v1`；
- Restore 到不存在的 Session ID 时，Snapshot 必须已包含
  `outcome-reporting-v1`；
- Existing Legacy Lineage 的 Restore/Replay/Exact Retry 仍可用；
- `EngineOptions.AllowLegacySessionCreation` 是嵌入式 Runtime 的显式迁移逃生口；
  随附 Sidecar 永不启用它。

新集成不得使用该逃生口。若需迁移语义，应创建不同的 Baseline Session，并按
Migration Guide 所述游戏侧 Transaction 移动权威状态；绝不能编辑 Event Log
或 Snapshot Feature List。

## 可选 Capability Matrix

正确的 Transaction Authority 不再可选。剩余 Switch 是 Capability，只有游戏
实现对应契约时其 Endpoint/Data 才有意义：

| Optional Feature | 与哪些能力独立 | 游戏侧要求 |
| --- | --- | --- |
| `memory-archive-v1` | 所有其他 Optional Feature | 接受确定性有损 Summary，并制定 Retention/Privacy Policy |
| `belief-conflicts-v1` | Memory Archive、Goal、Activity、Arbitration | 保存带来源的冲突 Claim |
| `goal-candidates-v1` | 与 Memory/Belief/Activity 独立；可与 Arbitration 组合 | 提供完整 Candidate Goal，只在 Accepted Outcome 后采用 |
| `actor-activity-v1` | Memory/Belief/Goal/Arbitration | 持久化 Region 与 Awake/Dormant Transition |
| `arbitration-v1` | 与 Memory/Belief/Activity 独立；可与 Candidate Goal 组合 | 跟踪 World Revision，并应用一次 Atomic Batch Outcome |

`TestSafeBaselineSupportsEveryOptionalFeatureCombination` 对强制基线加五个
Optional Feature 的全部 32 种组合执行结构覆盖。各 Feature Test 再覆盖启用和
禁用时的 Endpoint 行为；State Invariant Test 覆盖 Outcome Reporting、
Arbitration、Candidate Goal、Activity 与 Conflicting Belief 的交互。Legacy
Test 使用显式迁移选项，并单独验证 Replay/Exact Retry。

SDK 的 `full` Preset 用于 Conformance 和高级集成，会启用全部 Optional
Capability；它不是推荐配置。应从 `safeBaseline` 开始，只在游戏确实需要并持久化
对应契约时加入 Capability。

# Rin v0.5 Living Worlds 实施计划

[English](living-worlds-v0.5-plan.md) | [简体中文](living-worlds-v0.5-plan.zh-CN.md)

状态：已批准的实施基线

## 1. 目标

Rin v0.5 将当前兼容单角色的运行时扩展为一个小型、与引擎无关的 Living
World 基础，同时不把游戏世界权威交给模型。该版本必须支持长期角色记忆、
互相冲突的私有认知、有界自主目标、区域感知的 Actor 调度、多角色仲裁和
可检查回放。

不变量保持为：

```text
模型或确定性策略 -> 提案
游戏规则         -> 应用或拒绝
Rin              -> 记录观察到的结果
```

首个生产消费者仍是 `ai-galgame`，但每个新契约都以与引擎无关的方式定义，
并在游戏适配器使用前由 Go 测试覆盖。

## 2. 约束

- 保持 `rin.protocol/v1`；新增内容使用可选字段和新端点。
- 现有 create/observe/propose/commit/snapshot 请求继续有效。
- 新状态字段使用 `omitempty`，使旧 Snapshot hash 仍可验证。
- Living World 行为通过 Session feature flag 启用。旧 Session 保留 v0.4
  的保留和调度行为。
- 核心继续只使用 Go 标准库并保持无 CGO。
- 模型不能在游戏提供的契约外创造可执行动作、目标、Goal、文件、工具或
  游戏状态修改。
- 玩家文本、Prompt、供应商响应和凭据不会写入运维日志或错误消息。
- 游戏渲染、导航、物理、战斗、背包、任务、同意、购买和 Canon 剧情状态
  继续由引擎拥有。

## 3. Feature 协商

`CreateSessionRequest.features` 接受一组有界标识符：

| Feature | 用途 |
| --- | --- |
| `memory-archive-v1` | 确定性情节记忆压缩与摘要召回 |
| `belief-conflicts-v1` | 保留互相矛盾的 Actor 本地说法 |
| `goal-candidates-v1` | 允许 Policy 选择有界候选 Goal |
| `actor-activity-v1` | 持久化区域与 dormant/awake Actor 活动 |
| `arbitration-v1` | 记录确定性的多 Proposal 仲裁 |

未知 Feature 会让 Session 创建失败。`/health` 会公布支持列表，使适配器
可以关闭失败或省略不支持的 Feature。

当前 `ai-galgame` 接入启用 Memory Archive 与 Belief Conflict。在内容包
提供显式候选目标和多角色场景前，不启用自主候选 Goal 或 Arbitration。

## 4. 记忆模型

### 4.1 情节记忆

`ActorState.memories` 保留近期、可带 Quote 的事件流。现有按重要度、
近期性、Tag、Quote 和 Recall Count 的检索评分继续可用。

启用 `memory-archive-v1` 且超出情节记忆上限时：

1. 从记忆较旧的一半中确定性选择一批低显著性项。
2. 在仍有较低显著性候选时保留重要度为五的事件。
3. 创建一级 `MemorySummary`，包含有界拼接摘要、合并 Tag、来源事件 ID、
   Tick 范围、重要度和压缩原因。
4. 只删除该摘要所代表的来源情节。
5. 摘要容量超限时，把最旧摘要合并到更高层级，而不是静默删除。

Summary ID 是内容 Hash，因此 Replay 不受 Map 遍历顺序或墙上时间影响，
会生成同一 Archive。

`MemorySummary.reason` 解释为何细节被压缩。来源事件 ID 和 Tick 范围让
开发者追踪保留内容，而无需存储无限原文。Policy 检索可返回 Episode 或
Summary ID；接受 Commit 会更新两类记忆的 Recall Counter。

### 4.2 兼容性

未启用 `memory-archive-v1` 的 Session 继续像 v0.4 一样保留最新 128 条
情节记忆。旧事件日志保持历史 Replay 语义，除非新建 Session 显式选择。

## 5. Actor 本地认知

Observation 可见性仍是主要隐私边界：只有位于 `observer_ids` 以及 Fact
可选 Visibility List 中的 Actor 才能获得对应 Memory 或 Claim。

启用 `belief-conflicts-v1` 后，每个 `(subject_id, predicate)` 保存一个
有界 `BeliefSet`：

- 所有不同的近期 Claim 及其来源事件 ID；
- Confidence 和观察到的 Revision；
- 当前选中的 Claim；
- 不同 Object 共存时的显式 `conflicted` 标记。

现有 `ActorState.beliefs` Map 保留为选中 Claim 的兼容投影。选择过程确定：
先比较更高 Confidence，再比较更新 Revision，最后按 Object 字典序。
Rin 不会悄悄把 Rumor 变成世界真相，也不会把一个 Actor 的 Claim 复制给
另一个 Actor。

模型 Prompt 只获得请求 Actor 的有界 Selected Belief 和 Conflict Summary，
不会引入全知全局状态。

## 6. 有界自主 Goal

`ProposeRequest.candidate_goals` 可以包含零个或多个完整 `Goal` 模板。
Policy 可以引用：

- Actor 现有的 Active Goal；或
- 本次请求提供的一个 Candidate Goal。

选中 Candidate 后，`ActionProposal.proposed_goal` 嵌入完全相同的模板。
只有游戏接受关联 Action Commit 后，Goal 才进入 Actor 状态。被拒绝或过期
的 Proposal 永远不会创建 Goal。

这让角色可以主动，同时保持权威。游戏可以提供“询问损坏的相机”或“完成
桥梁维修”等 Goal，但模型不能创建游戏未公布的购买、亲密升级、Quest、
Target 或不可逆目标。

## 7. World Revision 与多角色仲裁

### 7.1 World Revision

Event Log Revision 在每个持久化事件（包括 Proposal）后变化。多角色工作
还需要一个只在可观察世界状态变化时改变的 Revision，因此引入
`SessionState.world_revision`：

- 在 Create、Observe、接受或拒绝 Commit、Actor Activity 和 Restore 时
  递增；
- 不会仅因另一个 Actor 创建 Proposal 或 Arbitration Record 而递增；
- 复制到每个新 Proposal。

这样，多个 Actor 可以针对一个稳定世界状态并行提案。普通单 Commit 在
无关 Proposal 之后仍有效，但在 Observation、Activity 变化、Restore 或
另一个已提交结果后变为过期。

### 7.2 仲裁

`POST /v1/world/arbitrate` 接收 Pending Proposal ID 和一组有界 Exclusive
Target ID。Rin 按 Active Goal Priority、Proposal Tick、Actor ID 和
Proposal ID 确定性排序，返回：

- `selected`：没有排名更高的 Proposal 占用同一 Exclusive Target；
- `deferred`：更早的 Winner 已占用至少一个 Target；
- 面向玩家的原因和冲突 Proposal ID。

Arbitration 是持久化的调试建议，不会执行 Action 或解决 Proposal。

`POST /v1/action/commit-batch` 在一个原子事件中记录基于同一 World
Revision 的 Proposal 结果。游戏必须先通过自己的系统应用所有选中动作，
再 Commit。任何 Item 无效或过期都会拒绝整个 Batch。

## 8. 区域活动与调度

`POST /v1/session/activity` 持久化有界 Actor 更新：

- Actor ID；
- Region ID；
- `awake` 或 `dormant` 状态；
- 游戏编写的原因和 Tick。

Dormant Actor 不会出现在 `/v1/scheduler/due`，游戏唤醒前也不能 Propose。
`DueAgentsRequest.region_ids` 可选地把查询限制到当前加载区域。空 Region
Filter 保持现有行为。

游戏在区域加载/卸载或模拟日程变化时更新 Activity，而不是每个渲染帧。
人群可继续使用 Deterministic Policy，附近具名 Actor 使用 Model Policy。

## 9. Timeline 与 Replay

两个只读操作支持调试：

- `/v1/session/timeline`：有界事件 Header 和安全结构元数据；
- `/v1/session/replay`：重建并验证指定 Revision 的状态。

Timeline 响应省略 Observation Summary、Quote、Prompt、Provider Content、
Token 和 Credential。Replay 返回协议状态，可能暴露已存在于经过鉴权
Session 中的剧情数据，因此远程端点必须沿用现有 Bearer Token 边界。

`rin inspect` 打开数据目录，通过正常 Runtime Replay 验证每条 Hash Chain，
并输出 JSON Session Summary。可选 Revision 使用与 HTTP 端点相同的 Replay
实现。

## 10. 引擎适配器

### Ren'Py

- 为 Activity、Arbitration、Batch Commit、Timeline 和 Replay 增加普通
  Dictionary 方法。
- 所有 HTTP 和 Polling 对象只保留在进程内。
- `ai-galgame` v1.2 内容只启用 Memory 和 Belief Feature。
- Rin 禁用或不可用时保留 Authored Fallback。

### Godot 4

- 为 Activity、Due-Agent Query、Arbitration 和 Batch Commit 增加
  Coroutine Helper。
- 导航、动画、战斗、背包和 Scene Tree 修改保留在 Godot。

### Unity

- 为相同端点增加可序列化 Request/Response DTO 和 Coroutine 方法。
- 继续使用 `UnityWebRequest` 和无额外 Package 的有界下载。

适配器不会每帧运行 Agent Loop。引擎拥有 Simulation Tick，并决定 Actor
何时值得提交 Proposal Job。

## 11. 实施阶段与 Commit

### 阶段 A：计划与兼容契约

- 添加本文档并更新 Roadmap。
- 记录基线 Go 和游戏测试结果。
- Commit：`docs: plan living worlds runtime`。

### 阶段 B：认知

- 添加 Feature 协商和可选协议字段。
- 实现 Memory Archive 压缩、Summary Retrieval、Snapshot 验证和确定性
  Replay 测试。
- 实现 Belief Set 和冲突 Claim Prompt Projection。
- Commit：`feat: add long-term actor cognition`。

### 阶段 C：自主与世界协调

- 添加 Candidate Goal 与 Commit 时采用。
- 添加 World Revision 语义。
- 添加 Actor Activity、Region Filter、Arbitration 与原子 Batch Commit。
- Commit：`feat: coordinate living world actors`。

### 阶段 D：可观测性与适配器

- 添加 Timeline/Replay API 和 `rin inspect`。
- 扩展 Ren'Py、Godot、Unity 适配器与示例。
- 更新 Protocol、Architecture、RPG、Model Policy 和 Security 文档。
- Commit：`feat: add living world tooling and adapters`。

### 阶段 E：游戏接入

- 在 `ai-galgame` 创建新 Rin 周目 Session 时启用兼容认知 Feature。
- 扩展 Compatibility Vector 和进程级 Integration Check。
- 保持旧存档和 Classic Mode 不变。
- 在游戏仓库 Commit：`feat: enable Rin living memory`。

## 12. 自动验证

Rin 验收要求：

- `go test ./...`；
- `go test -race ./...`；
- `go vet ./...`；
- 确定性 Replay 生成相同状态和 Summary ID；
- 旧 v0.4 Fixture 与 Snapshot 仍可验证；
- Memory 不超过 Episode 或 Archive 上限；
- 私有 Claim 不出现在未列出的 Actor；
- 冲突 Claim 经过 Snapshot/Restore 后仍存在；
- Candidate Goal 只由 Accepted Commit 添加；
- Dormant Actor 不会到期也不允许 Propose；
- Arbitration 在打乱输入时仍保持确定顺序；
- Batch Commit 原子执行并拒绝混合 Revision；
- Timeline 输出不包含 Observation Quote 或 Summary；
- macOS arm64/amd64、Windows amd64、Linux amd64 构建成功；
- Ren'Py 适配器测试和 Compatibility Vector 通过。

游戏验收要求：

- 完整 Python Suite；
- Rin Boundary 和 Source Scan；
- 无 Key 的真实进程 Session -> Observation -> Proposal -> Arbitration ->
  Commit -> Snapshot -> Restore 检查；
- SDK 可用时运行 Ren'Py lint 和 compile。

## 13. 因锁屏延期的人工验证

- 在支持的桌面分辨率检查 Memory 与 Relationship Screen。
- 跨多个章节在线游玩，确认召回台词自然。
- 存档、创建不同未来、读档，确认 Memory 回退。
- 请求期间停止 Rin，确认离线继续仍然响应。
- 评估自主问题是否多样且不过度打扰。
- 运行至少有三个竞争 NPC 的小型 Godot 或 Unity 场景。

## 14. 发布与回滚

- 现有 Session 不会自动获得 Living World Feature。
- 游戏移除 Feature Identifier 后，新 Session 恢复 v0.4 语义。
- 不通过 Migration 原地重写 JSONL 事件。
- 新端点失败不会破坏现有 Session，因为所有写入都先验证，再原子追加一条
  Hash-Chained Event。
- 游戏可随时禁用 Rin 并继续使用 Authored Content。

## 15. 停止条件

当全部自动检查通过、每阶段都有本地 Commit、`ai-galgame` 可以选择启用
认知而不改变 Canon 剧情权威，并且只剩 GUI、跨引擎场景、长时间试玩和
人工质量检查时，实施完成。

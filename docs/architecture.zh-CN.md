# 架构

[English](architecture.md) | [简体中文](architecture.zh-CN.md)

Rin 是管理智能体状态与决策的引擎中立控制层，不是模拟或修改游戏世界的权威。

## 权威边界

```mermaid
flowchart LR
    G["Game engine\nworld authority"] -->|Observation| R["Rin runtime\nmemory + goals + policy"]
    R -->|ActionProposal| V["Schema + boundary + freshness validation"]
    V -->|candidate action only| G
    G -->|Applied/rejected outcome report| R
    R --> E["Hash-chained event log"]
    R --> S["Verified snapshot"]
    R -->|"bounded prompt packet"| P["Optional model provider"]
    P -->|"structured draft"| V
```

游戏引擎始终拥有世界权威。Rin 不直接修改场景、任务、物品、战斗、角色位置、关键选择或存档。Policy 只能从本次请求的 `candidate_actions` 中选择一个动作；运行时还会检查角色、目标、记忆引用、边界、会话 revision 和内容绑定。

## 组件

### 协议

`protocol` 是唯一需要被其他语言复刻的层。所有请求显式携带 `rin.protocol/v1`，未知 JSON 字段会被 HTTP 层拒绝，标识符禁止路径分隔符。

### 运行时

`runtime.Engine` 是确定性状态机。每个会话单独加锁；Policy 在锁外执行，因此远程模型变慢不会阻塞新的观察或读状态。旧会话继续用 revision/head hash 判断应用前的 Proposal 是否过期；显式启用 `outcome-reporting-v1` 的会话采用下文“游戏先处理、再回报”和发生时间合并语义。启用 `arbitration-v1` 的会话使用随权威 Observation 和 Outcome 结算前进的 `world_revision`，因此同一轮多个角色可以并行提出动作。游戏已经处理的 Outcome 即使延迟到达也会被记录，不再作为应用前 Proposal 重新判断新鲜度。

详细记忆保持固定窗口；`memory-archive-v1` 将最旧批次压成带来源 ID、tick 范围和原因的确定性摘要，并在摘要达到上限后继续分层合并。`belief-conflicts-v1` 为每个角色保留最多八条来源声明，同时维持旧 `beliefs` 字段作为当前选中投影。两者都完全由事件重放恢复，不依赖向量数据库。

### 策略

Policy 接口只返回 `ProposalDraft`。运行时不信任实现：动作必须来自白名单，记忆和目标 ID 必须真实存在，文本长度与 stance 必须合法。

内置 `policy.Deterministic` 是离线基线：

1. 标签命中边界时只选择对应的 `refuse`、`redirect` 或 `wait` 动作。
2. 否则优先服务高优先级主动目标。
3. 用重要度、近期性、标签和召回次数选择最多三条记忆。
4. 对重复动作降权，以固定 seed 和请求上下文确定性打破平局。

在线模型 Policy 只替换第 2–4 步，不绕过运行时验证器。

### 模型策略

模型 Policy 只构造最小上下文包。系统指令与游戏数据分成两个 message，玩家输入、剧情文本和内容包字段全部位于 `untrusted_game_data`；同时给出独立 `contract`，列出唯一合法的 action、memory 和 goal ID。供应商即使不支持严格 JSON Schema，返回结果仍会在本地执行 unknown-field、类型、长度和 ID 白名单校验。

角色边界在调用供应商之前本地处理。触发边界时直接使用 `boundary-guard`，不会依赖模型自行拒绝。

### 供应商韧性

OpenAI-compatible 客户端由标准库实现。每次调用具有 attempt timeout 和 total timeout，只重试网络、429、408 和 5xx 等暂时错误；连续失败会打开 circuit breaker，开放期直接进入离线回退。响应正文、Prompt 和 Key 不写入错误、日志或状态。

模型 Draft 按 Session head hash、Actor 和语义请求建立有界内存缓存。相同 key 的并发调用合并成一次供应商请求；状态变化后 head hash 改变，旧结果不会命中新世界状态。

### 异步任务

`jobs.Manager` 使用有界 worker 和 queue。游戏先提交 `/v1/jobs/propose`，继续渲染与接收输入，再通过 GET 轮询。若思考期间 Session 变化，Job 结束为 `stale`，不会写入旧提案；取消会沿 context 传递到 HTTP Provider。

Job 元数据只在进程内保留，成功 Proposal 本身已进入事件日志。Sidecar 重启后，客户端可用同一 `request_id` 重新提交，Engine 会幂等返回已生成 Proposal。

### 结构化生成

`generation.Manager` 为游戏拥有的受限 Prompt 提供另一条有界异步队列。它复用同一个 resilient Provider，但不接触 Session 状态，也不直接写事件日志。请求按完整 payload 幂等、按去掉 request ID 后的语义内容短期缓存；取消沿 context 传播到 Provider。

Generation 只保证传输、大小和顶层 JSON Object 合法。各游戏仍必须验证自己的 `ScenePacket`、任务、对白或结局 Schema。若验证失败，游戏丢弃结果并使用本地内容；模型输出永远不会自动成为 Canon。

### 游戏适配器

Ren'Py、Godot 和 Unity 适配器只转换 JSON/HTTP 与各自的异步机制，不复制 Runtime 状态机。在线结果带 `committable=true`，表示游戏处理后可向 Sidecar 回报该 Proposal ID，而不是 Rin 授权执行。只有确定在线提交从未创建 Proposal（例如 Sidecar 已禁用或初始连接被拒绝）时，适配器才能从游戏本次候选列表选择 authored fallback，并标记 `committable=false`；提交、轮询、超时或取消结果尚未确认时必须 fail closed。游戏不得把本地 `offline.*` ID 发给 `/commit`。

Ren'Py worker registry、Godot `HTTPRequest` 和 Unity coroutine 都只存在于进程内。游戏存档保存 Snapshot 与普通结果，不保存线程、Future、Socket、HTTP 对象或 API Token。

### 多角色协调

候选目标仍由游戏提供上限和语义范围，Policy 只能建议采用；启用 `outcome-reporting-v1` 后，只有游戏已经应用并以 accepted Commit 回报的目标才写进 Actor。Activity 状态由游戏的区域或模拟系统更新，Dormant 角色不会自行唤醒。Arbitration 对同一 world revision 的 Proposal 做稳定排序并记录冲突，但不执行动作；游戏可以调整、拒绝，再以原子 Batch Commit 汇报实际结果。完整事务与 Outbox 规则见[动作结果记账](outcome-reporting.zh-CN.md)。

这使 Rin 可以服务视觉小说、RPG NPC 和模拟居民，同时不承担寻路、碰撞、任务规则或 Scene Tree 等引擎职责。

### 可观测性

Timeline 只从事件 payload 提取 ID 和枚举状态，不返回玩家原话、剧情摘要、Commit outcome 或模型内容。Replay 则运行同一个 reducer 到指定 revision，生成完整且可验证的 Snapshot，不写回 Store。`rin inspect` 复用这两条路径输出机器可读诊断；打开数据目录时仍会验证全部事件 hash chain。

### 存储

文件存储结构：

```text
rin-data/
└── sessions/
    └── session.id/
        ├── events.jsonl
        └── snapshot-<revision>-<hash>.json
```

事件哈希覆盖 sequence、type、request ID、记录时间、上一事件哈希和 payload。启动时完整重放并验证；任何断链、改写或未知事件类型都会阻止会话加载。快照通过同目录临时文件、`fsync` 和 rename 写成按 revision/hash 命名的不可变文件，权限为 `0600`，不依赖各平台不同的覆盖 rename 行为。

文件 Store 是单写者设计：同一数据目录同时只能由一个 Rin 进程使用。需要多实例时应实现外部协调的 Store，而不是共享 JSONL 目录。

## NPC 调度

每个 Actor 声明 `think_every_ticks`。游戏应用动作并以 accepted Commit 回报后，
`next_think_tick = max(current, commit.tick + think_every_ticks)`，因此延迟结果
不会让调度倒退。游戏可在区域进入、回合结束、分钟推进或关键事件后调用
`/v1/scheduler/due`，不应在渲染帧中轮询模型。

紧急事件可在 propose 请求中设置 `urgent: true`，但它只绕过调度时间，不绕过边界和动作白名单。

## 存档与回滚

- 游戏存档应保存 Rin 返回的 Snapshot，而不是内部文件路径。
- Snapshot 带内容包 Binding 和状态哈希。
- 启用 `outcome-reporting-v1` 后，Restore 会保留 pending Proposal，既让存档中
  尚未处理的 Proposal Attempt 能恢复，也让 Outcome Outbox 能补报读档前已经
  应用的动作。恢复出的 Proposal 不授权执行；游戏必须依赖持久化 Attempt 和
  applied-operation marker 区分两种状态，重新校验尚未处理的动作，并且绝不
  重做已经处理的动作。
- 未启用该 Feature 的 Session 保留旧版 Restore 行为并清空 Proposal。
- 已提交事件、记忆、事实、目标进度和调度 tick 会恢复。
- 新数据目录可以导入 Snapshot；此时本地事件链从一条 restore 事件开始。
- 重复载入同一存档时，调用方应让 restore request ID 同时绑定 Snapshot hash 与当前 Sidecar head，以区分网络重试和真正的再次回档。

## 模型接入规则

推荐把模型调用实现为另一个 `Policy`，或由上层 Showrunner 先生成结构化 Draft。供应商请求必须有超时和取消，API Key 只从进程环境或宿主安全存储读取。模型不接触事件文件、快照路径、游戏脚本和任意工具执行。

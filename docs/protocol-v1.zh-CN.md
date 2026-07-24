# Rin Protocol v1

[English](protocol-v1.md) | [简体中文](protocol-v1.zh-CN.md)

本文定义 Rin 与游戏自有适配器之间稳定的 HTTP 与状态契约。

## Envelope 封装

请求使用 `Content-Type: application/json`，默认最大请求体为 32 MiB；各类数组
和字段仍有更小的结构上限。所有随附客户端的默认最大响应正文同样是 32 MiB。
Inline Snapshot 的 compact JSON 另有 16 MiB 上限，为响应 envelope、Restore
元数据和持久 EventRecord framing 预留传输空间。Rin 会返回
`413 snapshot_too_large`，绝不会截断 Snapshot。Identifier History 会随
Session lineage 增长；超过 inline 上限的 lineage 不能使用 Snapshot 或 Replay
JSON 传输。当前不提供流式 Snapshot 传输。成功响应：

```json
{"ok":true,"data":{}}
```

失败响应：

```json
{
  "ok": false,
  "error": {
    "code": "invalid_request",
    "message": "must be between 1 and 5",
    "field": "importance"
  }
}
```

除无请求体的 Job 查询与取消接口外，每个 JSON 请求体都必须包含：

```json
{"protocol_version":"rin.protocol/v1"}
```

ID 长度为 1–96，只允许字母、数字、`.`、`_`、`-`，从源头阻止路径穿越并保持 Windows 文件名兼容。

## 持久 Request 与 Event 身份

每个持久修改 Session 的 mutation 都带调用方生成的 `request_id`。在同一
Session lineage 内，包括重启、Restore generation 和后来被 Restore 放弃的
分支，Rin 会把该 ID 永久绑定到：

- mutation 类型；
- 完整请求经严格解码为协议类型、再做 canonical JSON 编码后的 SHA-256 digest；
- 首次持久操作结果。

因此 Object 成员顺序和无意义 JSON 空白不影响身份；数组顺序及每个 typed 字段
都会影响。完全相同的重试不会再次修改状态，而是返回首次结果。Mutation 响应
保留首次事件的 `revision` 与 `head_hash`，Proposal 和 Arbitration 则返回原始
typed 结果；只有重试响应会设置 `duplicate=true`。这些 revision/head 是首次
操作的确认，不是 Session 当前 head；需要当前 State 时应调用
`/v1/session/get`。同一 `request_id` 用于其他类型或 payload 时返回
`409 request_id_conflict`。

Observe、Commit 与 Commit Batch 的每个 Item 共用一个永久的
`(session_id, event_id)` 命名空间。Event ID 一旦在该 lineage 中被接受，其他
请求再次使用就返回 `409 event_exists`。Event ID 不是第二个幂等键：只有携带
原始 `request_id` 的完全相同重试才返回 duplicate success。不同 Session 可以
使用相同 ID，但为了存档可移植性，仍建议调用方生成全局唯一 ID。

有界 `state.receipts` map 只是兼容与诊断所用的热投影，不是权威幂等索引。
Receipt、Proposal、Arbitration、Memory、Summary 或其他 State 投影被淘汰，都
不会让其 Request/Event ID 重新可用。

Proposal Job 与 Generation Job 记录遵循下文独立的、有界进程内保留规则；它们
本身不是持久 Session mutation。

如果 append 或首次 Create/fresh Restore 写入失败，且 Rin 无法证明事件是否
已经持久化，非 Proposal 端点会返回 `mutation_outcome_unknown`。这表示结果
未决，既不是确认失败，也不是确认成功。调用方必须保留该 Operation，并以完全
相同的 mutation 类型、typed payload、`request_id` 和所有 Event ID 重试。同一
ID 下改变请求会返回 `409 request_id_conflict`；在 exact retry 确认 tail 前，
该 Session 的其他 mutation 都会被阻塞，并返回
`409 mutation_outcome_unknown`。首次暴露 Store 未决状态的 Operation 通常
返回 HTTP `500`；确认仍不可用时，exact retry 也可能再次返回该状态。恢复成功
后可能是普通响应，也可能是 duplicate，取决于 Rin 最终确认的持久证据；无论
哪种情况，都不会再次应用同一逻辑 mutation。

Proposal append 继续使用兼容错误码 `proposal_outcome_unknown`，并遵循相同的
fail-closed exact-retry 规则。不得把任一种未决请求换成新 ID，也不得让离线
fallback 越过它。

## 创建会话

`POST /v1/session/create`

```json
{
  "protocol_version": "rin.protocol/v1",
  "request_id": "create.playthrough-1",
  "session_id": "playthrough-1",
  "binding": {
    "game_id": "my-game",
    "content_id": "base-story",
    "content_version": "1.0.0",
    "content_hash": "sha256:..."
  },
  "seed": 42,
  "features": ["outcome-reporting-v1", "memory-archive-v1", "belief-conflicts-v1"],
  "actors": [
    {
      "id": "npc.mira",
      "kind": "npc",
      "display_name": "Mira",
      "traits": ["curious", "careful"],
      "boundaries": [
        {
          "id": "boundary.privacy",
          "description": "Do not reveal private records.",
          "trigger_tags": ["private"],
          "response": "refuse"
        }
      ],
      "goals": [
        {
          "id": "goal.connect",
          "description": "Build trust through specific actions.",
          "priority": 4,
          "preferred_actions": ["talk"],
          "progress": 0,
          "target_progress": 3,
          "status": "active"
        }
      ],
      "think_every_ticks": 5,
      "enabled": true
    }
  ]
}
```

Binding 防止另一版本剧情或 Mod 的状态被静默恢复到当前游戏。

`features` 是新会话显式选择的兼容开关，可用值由 `/health` 的 `features` 返回：

- `memory-archive-v1`：将超出详细窗口的记忆压缩为确定性分层摘要；
- `belief-conflicts-v1`：保留角色私有的互相矛盾说法及来源；
- `goal-candidates-v1`：允许 Policy 从本次请求给出的候选小目标中提出一个；
- `actor-activity-v1`：启用区域和 awake/dormant 生命周期；
- `arbitration-v1`：启用 world revision、多角色仲裁与原子批量 commit；
- `outcome-reporting-v1`：让游戏成为结果的唯一权威，允许延迟回报，并按游戏
  发生时间合并 Fact、Goal、记忆、动作和调度。

未启用某 Feature 的旧 Session 保持对应的历史 reducer 行为与重放结果；
`outcome-reporting-v1` 尤其不会自动加入已有事件日志。启用 Feature 后返回状态
可能增加可选发生时间元数据；JSON 解码器必须忽略不认识的字段。

## 提交观察

`POST /v1/session/observe`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "observe.event-18",
  "event_id": "event-18",
  "tick": 18,
  "observer_ids": ["npc.mira"],
  "source": "game",
  "kind": "dialogue",
  "summary": "The player waited instead of demanding an answer.",
  "quote": "Take your time.",
  "tags": ["conversation", "trust"],
  "importance": 4,
  "facts": [
    {
      "subject_id": "player",
      "predicate": "respected_boundary",
      "object": "event-18",
      "visibility": ["npc.mira"],
      "confidence": 100
    }
  ]
}
```

只有 `observer_ids` 中的角色获得这段记忆。Fact 若带 `visibility`，只写入
名单中的观察者，避免 NPC 知道未见过的事情。

启用 `outcome-reporting-v1` 后，返回状态里的 Fact 会由 Rin 使用外层请求 tick
写入 `observed_tick`；请求中应省略该字段或保持零值。此时权威 Observation
可以在 Session tick 已前进后到达，包括存档恢复后的对账。Rin 保留原始
`tick`，按发生时间排列记忆，并阻止旧 Fact 替换更新值。未启用该 Feature
的 Session 保留旧版 tick 单调约束和到达顺序语义，也不填充
`observed_tick`。

## 生成提案

`POST /v1/agent/propose`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "propose.turn-19.mira",
  "actor_id": "npc.mira",
  "tick": 19,
  "intent": "Choose how to respond.",
  "tags": ["conversation"],
  "candidate_actions": [
    {"id":"talk","kind":"dialogue","description":"ask one honest question"},
    {"id":"refuse","kind":"refuse","description":"protect a private boundary"},
    {"id":"wait","kind":"wait","description":"stay silent for now"}
  ],
  "candidate_goals": [
    {
      "id": "goal.ask-about-photo",
      "description": "Find a calm moment to ask about the old photograph.",
      "priority": 2,
      "progress": 0,
      "target_progress": 2,
      "status": "active"
    }
  ]
}
```

返回的 Proposal 带：

- `based_on_revision` 和 `based_on_head_hash`：生成依据。
- `action`：原样取自游戏候选动作，Policy 不能添权。
- `recalled_memory_ids`、`goal_id`：可审计依据。
- `rationale`：给 UI 使用的一句角色化说明，不是模型隐藏推理。
- `status: pending`：Rin 尚未收到游戏处理结果；它不是等待 Rin 激活的动作。
- `policy_source`：`model`、`model-cache`、`boundary-guard`、`deterministic-fallback` 或离线来源。

Policy 运行期间不会持有会话锁。如果新观察先到达，调用返回 `state_changed`；客户端应以新的 `request_id` 重试。

候选目标只在启用 `goal-candidates-v1` 时允许，最多 8 个。Policy 不能凭空创建目标，只能选已有目标或本次候选目标；候选目标随 Proposal 返回，只有 Proposal 被接受后才进入 Actor 状态，拒绝或过期不会留下目标。

在线模型不建议由游戏主线程直接调用本端点，应使用异步 Job API。

## 异步提案任务

提交使用与 Propose 相同的请求体：

`POST /v1/jobs/propose`

服务立即返回 `202 Accepted`：

```json
{
  "ok": true,
  "data": {
    "protocol_version": "rin.protocol/v1",
    "job_id": "job....",
    "status": "queued",
    "duplicate": false
  }
}
```

查询不需要请求体：

`GET /v1/jobs/{job_id}`

状态为 `queued`、`running`、`succeeded`、`failed`、`stale` 或 `canceled`。成功时 `proposal` 字段包含正常 ActionProposal；失败时只返回安全错误码，不包含供应商正文。

客户端把终态 `failed` 当成“确认没有 Proposal”之前，必须检查
`job.error.code`。`proposal_outcome_unknown` 表示 Rin 无法判断或确认
Proposal 事件是否已经持久化；即使 Job 已是终态，该错误也不是
no-Proposal 证明。游戏必须保留持久 Proposal Attempt，用完全相同的
`request_id` 和 payload 重新 POST，再用返回的（通常不变的）Job ID 继续
GET。完成对账前不得执行离线 fallback，也不得开始其他 Session mutation。
同步调用第一次暴露这种不确定性时可能返回 HTTP `500`；被该不确定性阻塞的
其他 mutation 会以 HTTP `409` 返回同一错误码。

取消：

`DELETE /v1/jobs/{job_id}`

响应是稳定的终态。取消运行中的 Proposal Job 时会等待正在进行的 Engine
mutation 结算；若 Proposal 已赢得持久写入竞态，DELETE 会返回带 Proposal 的
`succeeded`，而不是把它隐藏成 canceled。客户端必须消费该响应。

Proposal Job 记录仍在当前进程中保留时，相同 Session 和 `request_id` 的重复
提交返回该 Job；payload 不同则返回 `request_id_conflict`。Job 元数据不持久，
可能在保留 TTL 后或重启时消失。其产出的 Proposal 是持久 Session mutation：
重新提交完全相同的请求时，即使需要重建进程内 Job，Engine 仍会返回原始
Proposal。Job 队列有界，满载时返回 `429 jobs_queue_full`。

## 结构化生成任务

结构化生成用于受限对白、场景、任务文本或结局呈现。它不读取或修改 Session，不产生世界事实，也不能替代 Proposal / Commit 权威边界。

`POST /v1/generation/jobs`

```json
{
  "protocol_version": "rin.protocol/v1",
  "request_id": "generation.scene-12",
  "kind": "scene",
  "context_hash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "messages": [
    {"role":"system","content":"Return one bounded scene JSON object."},
    {"role":"user","content":"{\"storylet_id\":\"scene-12\"}"}
  ],
  "temperature": 0.6,
  "max_tokens": 1024,
  "response_format": "json_object"
}
```

`kind` 允许 `director`、`story`、`scene`、`decision`、`ending`、`free-response`、`storylet-selection`。消息为 1–8 条，每条和总字符数有界；`context_hash` 是调用方对语义上下文生成的 SHA-256 标识，用于诊断和一致性检查。

提交立即返回 `202 Accepted`。查询与取消：

```text
GET    /v1/generation/jobs/{job_id}
DELETE /v1/generation/jobs/{job_id}
```

状态为 `queued`、`running`、`succeeded`、`failed` 或 `canceled`。成功结果包含 JSON Object 原文以及模型名、finish reason、token usage、`cache_hit` 等有界元数据。Rin 会再次解析输出，数组、纯文本、空内容、非法 UTF-8、NUL 和超出大小限制的内容均失败。

只有对应进程内记录仍被保留时，同一 `request_id` 与相同 payload 才返回同一
Job；相同语义但不同 ID 可以命中短期缓存。Generation Job 不写入事件日志；
Job 被淘汰或 Sidecar 重启后，相同请求可能再次调用 Provider 并得到另一结果。
游戏应先按自己的内容契约验证结果，再决定是否接受到 Canon。供应商失败不会
自动生成替代剧情，调用方必须提供离线内容。

## 提交结果

`POST /v1/action/commit`

Commit 是游戏应用或拒绝 Proposal 后的权威结果记账，不是执行许可。游戏必须
在自己的权威线程重新验证并处理动作，再发送 Commit。`accepted=true` 表示
动作已经实际生效；`accepted=false` 表示该动作效果没有发生。

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "commit.turn-19.mira",
  "proposal_id": "proposal....",
  "event_id": "event-19",
  "tick": 19,
  "accepted": true,
  "outcome": "Mira asked what the player wanted remembered.",
  "tags": ["conversation"],
  "facts": [],
  "goal_updates": []
}
```

接受提案会记录行动结果、更新调度、标记记忆被召回，并让关联目标自动前进 1。拒绝提案不会修改角色记忆、事实和目标；失败中观察到的新事实应另行
`observe`。启用 `outcome-reporting-v1` 时，拒绝结果必须省略 `facts` 与
`goal_updates`，接受结果对每个 Goal 最多包含一条 update。

启用 `outcome-reporting-v1` 后，`tick` 是动作发生或被拒绝的时间，不得早于
Proposal tick，但可以早于报告到达时的 Session tick。Proposal 产生后的 Rin
Revision 或 World Revision 即使已经前进，游戏已处理的 Outcome 仍会被记录。
已解决 Proposal 状态带有 `outcome_event_id` 和 `outcome_tick`，Fact 带有
`observed_tick`，Goal 带有 `updated_tick` 与未截断的
`progress_accumulator`；这些服务端字段用于在延迟到达时保持按发生时间
合并，并使正负进度增量不依赖到达顺序；`status_explicit` 标记状态是否由
游戏显式给出，`status_updated_tick` 和 `status_source_event_id` 独立排序
显式状态。未启用该 Feature 的 Session 保留旧版 stale/tick 校验和到达顺序
reducer。超时或暂时错误只能使用相同 `request_id` 重报，不能重新执行游戏
动作。完整合并、Outbox、延迟结果和迁移规则见
[动作结果记账](outcome-reporting.zh-CN.md)。

## Living World 协调

启用 `actor-activity-v1` 后，游戏在区域载入、卸载或模拟层级变化时调用：

`POST /v1/session/activity`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "activity.school-day-2",
  "tick": 80,
  "updates": [
    {"actor_id":"npc.mira","region_id":"school.roof","state":"awake"},
    {"actor_id":"npc.teacher","region_id":"school.office","state":"dormant"}
  ]
}
```

`state` 只能为 `awake` 或 `dormant`。Dormant 角色不会出现在 scheduler 中，也不能 propose。`/v1/scheduler/due` 可增加 `region_ids` 过滤。

启用 `arbitration-v1` 后，同一 world revision 可以为多个角色分别产生 Proposal，再调用 `POST /v1/world/arbitrate`：

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "arbitrate.turn-81",
  "tick": 81,
  "proposal_ids": ["proposal.mira", "proposal.teacher"],
  "exclusive_target_ids": ["prop.camera-1"]
}
```

结果以目标优先级、tick、actor ID、proposal ID 确定性排序，给出 `selected` 或 `deferred`。仲裁是建议记录，不直接改变游戏世界。游戏应用选中动作后，可用 `POST /v1/action/commit-batch` 一次提交每个角色最多一个结果。所有 Item 必须来自同一个原始 `based_on_world_revision`，但报告到达时当前版本可以已经前进；混合原始版本或任何无效 Item 都会拒绝整个批次，不产生部分修改。

## 调度器

`POST /v1/scheduler/due`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "tick": 24,
  "limit": 16,
  "region_ids": ["school.roof"]
}
```

按 `next_think_tick` 和 actor ID 稳定排序，便于回合制、区域制和时间片游戏使用。

## 状态闭包与有界保留

每个成功 Mutation 都会产生通过 Snapshot 与 Restore 同一套结构校验的完整
State。Reducer 在 Store append 前校验隔离的候选状态，因此无效转换不会留下
部分内存更新或持久化更新。Fact visibility 等动态引用必须指向 Session 中的
Actor。启用 `belief-conflicts-v1` 后，`beliefs` 与 `belief_sets` 必须具有完全
相同的 key，并投影同一个 selected Fact。

保留集合遵循以下协议上限：

| 集合 | 上限 | 满容量行为 |
|---|---:|---|
| Actor Goals | 32，包含不同 pending ProposedGoal 预留 | 拒绝新预留，不静默删除 Goal |
| Actor 详细 Memories | 128 | 启用 `memory-archive-v1` 时归档为 Summary；否则淘汰明细并移除其 recalled 引用 |
| Actor Memory Summaries | 32 | 确定性合并较旧摘要，level 在 16 饱和 |
| Actor Beliefs / BeliefSets | 256 个 key | 确定性淘汰最旧投影 key 及其配对 Set |
| Actor RecentActions | 32 | 保留按游戏发生时间排序的最新 outcome |
| Session Proposals | 64 | 只淘汰 resolved Proposal；全部为 pending 时 fail closed |
| Session Arbitrations | 32 | 保留最新记录 |
| Session Receipts | 1024 | 作为热投影保留最新 revision generation；永久 Request 身份另行保存 |

RecallCount 在 1,000,000 饱和。Memory 归档会把 Proposal 或 RecentAction 的
引用改写到替代 Summary ID；未启用归档时会移除不可用 ID。revision、tick、
selected belief source、Goal status source 与 visibility actor 等 Memory、
Summary、Belief、Activity、Goal 和 outcome 元数据都必须处在容器 State 的
有效范围内。State 中保留的 Proposal 与 Arbitration tick 不受 `state.tick`
上界限制，可以描述其后的工作；实时 Propose 与 Arbitrate 请求仍会拒绝 tick
倒退。Fact visibility 的 `null`/缺省与空数组属于相同 JSON 契约值。

所有有界 State 投影被淘汰后，Identifier History 仍会保留 Request 与 Event
ID。第一次恢复该 Session 时，会从事件日志或已校验 checkpoint 加 event tail
重建这份永久 ledger；Snapshot 与 Replay 也会在 `SessionState` 之外携带它。
应用仍应生成全局唯一 ID，从而避免旧 Snapshot 无法证明其完整导出前历史时
发生碰撞。

## Snapshot 与 Restore

Snapshot 请求和 Session State 请求结构相同：

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1"}
```

Restore：

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "request_id": "restore.save-slot-2",
  "expected_binding": {
    "game_id": "my-game",
    "content_id": "base-story",
    "content_version": "1.0.0",
    "content_hash": "sha256:..."
  },
  "snapshot": {
    "protocol_version": "rin.protocol/v1",
    "state_hash": "...",
    "state": {},
    "identifier_history": {
      "version": "identifier-history-v1",
      "coverage_complete": true,
      "requests": {},
      "events": {}
    },
    "identifier_history_hash": "..."
  }
}
```

`expected_binding` 是必填字段，必须来自运行中游戏的可信内容 manifest，
绝不能从待导入的 Snapshot 复制。Restore 会核对操作中的三方：
`expected_binding` 必须等于 `snapshot.state.binding`；目标 Session 已存在时，
其 Binding 也必须相同。Fresh target 由前两者的匹配建立新 Session Binding；
existing target 则要求三方全部匹配。任何不一致都返回
`409 binding_mismatch`。

Restore 也会拒绝 Session ID 不同或 checksum 错误的 Snapshot。Rin 在计算或
保存 Snapshot 前校验克隆的 State，因此每个成功返回的 Snapshot 都会立即
通过 `ValidateSnapshot`，并可导入空 Session 或尚未耗尽 revision 的匹配
Session。

`state_hash` 覆盖有界 `state`，`identifier_history_hash` 则独立覆盖
`identifier_history` 的 canonical JSON；两者都是 SHA-256 checksum。它们可
发现意外损坏和未同步更新 checksum 的修改，但不是签名，不验证来源真实性，
也无法阻止能修改 Snapshot 后重算 checksum 的一方。Snapshot 是可信、不透明
的序列化状态：不要接收不可信来源的 Snapshot，也不要编辑它；文件和正文均须
按事件日志同等级别保护。`expected_binding` 的权威来源始终是可信的运行时
manifest。

History 版本为 `identifier-history-v1`；其中 Request entry 保存 canonical
request digest 与原始结果坐标或 typed Proposal/Arbitration 结果，Event entry
保存每个已接受的 Event ID。`coverage_complete=true` 表示生成方知道完整的
imported lineage。只提供两个 History 字段中的一个、History 结构非法或
checksum 不匹配时，均返回 `400 invalid_snapshot`。

完整 Snapshot 的 compact canonical JSON 编码不得超过 16 MiB。Snapshot 创建
与 Replay 超限时会以 `413 snapshot_too_large` 原子失败；Restore 仅在完整请求
仍处于配置的请求正文上限内时返回该错误。若请求先超过外层正文上限（默认
32 MiB），解码阶段会优先返回 `413 body_too_large`。任何 State 或 Identifier
History 都不会被截断。所有随附客户端默认 32 MiB 响应上限，会为 Snapshot
外层的 envelope 和持久记录留出空间。

升级兼容是有意设计成非对称的：新的 HTTP Restore 请求缺少
`expected_binding` 时返回 `400 invalid_request`，调用方必须升级，并从可信
manifest 取得该字段。磁盘中既有的 `session.restored` 事件仍可正常重放，包括
内嵌 Snapshot 现已超过 16 MiB 的事件。重建 request identity 时，Rin 会重建
旧四字段 Restore request shape 并保留其 digest 语义。
旧事件的 Snapshot 仍在 inline 上限内时，携带可信且匹配
`expected_binding` 的新 schema exact retry 会识别旧 digest，并以 duplicate
返回原始结果。超限旧事件仍可从磁盘 Open 和 replay，但不能通过 inline API
重新传输；当前不提供流式 Snapshot 传输。

不带这两个字段的旧版 v1 Snapshot 仍可 Restore，但 Rin 会以
`coverage_complete=false` 导入。它只能从 Snapshot 中仍可发现的 ID 建立
索引；导出前已被淘汰的 ID 无法获知。不完整 coverage 会沿以后每次 Snapshot
与 Restore 合并永久传播，绝不会升级成 complete。导入后首次接受的每个新 ID
仍会永久保存。使用旧存档的应用因此必须继续生成全局唯一 ID。

Restore 会写入新的本地事件链 generation。保留的嵌套 revision 元数据会重基
到 Restore 事件；保留 Proposal 引用前一个本地 revision 与 head hash。Fresh
import 的 base 是 revision 0 和空 head hash。导入的历史 Receipt revision 会在
插入本次 Restore Receipt 前改为 0，因此已经装满 1,024 项的 map 不会淘汰刚
成功的操作。World revision 只前进、不回绕；导入已经达到最大值的 world
revision 时保持饱和，后续 world mutation 则 fail closed。

Restore 会先合并目标 Session 当前的 Identifier History 与 Snapshot History，
再加入本次 Restore request。即使 Restore 放弃当前分支，该分支中的 ID 仍作为
tombstone 保留。两份 History 中 verified mapping 不一致时，以
`409 identifier_history_conflict` 原子拒绝 Restore，绝不覆盖任一方。旧日志
中重复使用过的 ID，或者无法恢复完整 typed request digest 的记录，会作为
ambiguous tombstone 保持可读；以后任何使用这类 request ID 的尝试都会以
`409 request_id_conflict` fail closed。

从其他 Restore generation 导入的原始 duplicate 结果可能带来源 generation
的 revision/head，不一定对应新本地事件链中可 Replay 的事件。它仍是该操作的
不可变回执；当前本地 head 应通过 State 查询。

启用
`outcome-reporting-v1` 时，它会为两种持久恢复状态保留 pending Proposal：
游戏处理前收到但尚未结算的 Proposal Attempt，以及动作已经处理、但存档中的
Outcome Outbox 仍待补报的 Operation。恢复 Proposal 绝不授权游戏执行它。
游戏必须依靠存档中的 Attempt 与 applied-operation marker 区分两种状态；
尚未处理的动作要重新校验后再处理，已处理动作绝不能重复执行。未启用该
Feature 的 Session 保留旧版行为并清空恢复出的 Proposal。

当游戏反复载入同一存档时，Restore `request_id` 应同时绑定目标 Snapshot hash 和 Sidecar 当前 head hash。这样一次网络重试仍然幂等，而从后来状态再次读档会产生新的 Restore 事件并真正回退。

## Timeline 与 Replay

`POST /v1/session/timeline` 返回分页的事件类型、revision、hash、请求 ID、角色/实体 ID 和状态，不返回 Observation summary/quote、Commit outcome、Prompt 或模型正文：

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1","after_revision":0,"limit":50}
```

响应中的 `next_after_revision` 可用于下一页，`limit` 为 1–256。每个请求都会
捕获自己的 `current_revision`；分页之间发生的 mutation 可能推进 Session。
需要固定审计窗口的客户端应保存第一页的 `current_revision` 作为上界，并在该
revision 停止。随附 File Store 的健康 revision index 会让稳态每一页成为有界
range read，而不是完整事件日志重放。索引缺失或无效会产生一次全日志重建成本；
不支持 range 的自定义 Store 仍可能回退到完整 `Load`。

`POST /v1/session/replay` 会校验不晚于目标 revision 的最新可用内部 checkpoint，
再对剩余 event tail 执行正常 reducer 与 hash-chain 校验；没有可用 checkpoint
时回退到 genesis。随后返回不落盘的 Snapshot：

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1","revision":42}
```

Replay 会包含该 revision 已存在的角色记忆和剧情状态，因此沿用 Session API
的鉴权边界，不能当作脱敏日志接口。重放出的 `state` 对应指定 revision，但其
Identifier History 会携带完整的本地 lineage tombstone 集，包括在该 State
revision 之后首次使用的 ID。这种有意的不对称可防止 Restore 旧 Replay
Snapshot 后重新使用后来的 ID；Identifier result revision 因此可以大于
`snapshot.state.revision`。

Session 完成第一次 lazy load 后，Timeline 与 Replay 会在 mutation lock 下捕获
live head 和所需 Identifier History，随后释放锁，再执行 range I/O 与 reducer。
对尚未加载 Session 的第一次操作必须先串行完成其恢复。Checkpoint 只加速重建：
它是带版本、checksum 并锚定事件的派生缓存，不是协议 Snapshot 或事件日志权威。
运维方可调用 `Engine.VerifyAll()` 忽略 checkpoint，从 genesis 到 head 审计每个
Session。lazy recovery 成功后，如果没有选中可用 checkpoint，或
`head revision / 所选 checkpoint revision >= 2`，Runtime 会 best-effort
异步排队一个恢复出的 head checkpoint。Session read 返回时它可能尚未持久化；
checkpoint 构建或写入失败不会把已经成功的 read 变成错误。

Identifier History 及其中保留的 Proposal/Arbitration 结果会随成功 mutation
线性增长，也可能包含已从有界 cognition State 淘汰的历史模型文本，因此
Snapshot 文件和正文必须按事件日志同等敏感级别保护。完整 compact Snapshot
一旦超过 16 MiB，就不能 inline 返回或 Restore。当前不提供流式 Snapshot
传输；Identifier History 绝不会被静默截断。

## 常见错误

| HTTP | 错误码 | 含义 |
| --- | --- | --- |
| `400` | `invalid_json` / `invalid_request` | JSON 或字段契约错误 |
| `400` | `invalid_snapshot` | State 或 Identifier History 非法，或其 hash 不匹配 |
| `401` | `unauthorized` | Bearer Token 缺失或错误 |
| `404` | `session_not_found` / `unknown_actor` | 实体不存在 |
| `404` | `revision_not_found` | Replay revision 不存在 |
| `500` | `store_load_failed` | 无法读取持久 Session 存储；绝不能按 `session_not_found` 处理 |
| `500` | `replay_failed` | 持久 Session 恢复或 Replay 校验失败；绝不能创建替代 Session |
| `409` | `request_id_conflict` | Request ID ambiguous，或已绑定其他类型/payload |
| `409` | `event_exists` | Event ID 已在该 Session lineage 中保留 |
| `409` | `binding_mismatch` | 可信 `expected_binding`、导入 Snapshot 或 existing Session 的 Binding 不一致 |
| `409` | `identifier_history_conflict` | Restore 的两份 History 含不兼容 verified identity |
| `409` / `500` | `mutation_outcome_unknown` | 非 Proposal mutation 可能已持久化；保留它，并在任何其他 mutation 前只重试完全相同的请求 |
| `409` | `state_changed` / `proposal_stale` | Proposal 生成或应用前仲裁的基础状态已改变 |
| `409` | `proposal_base_mismatch` | Batch Outcome 混合了不同的原始 world revision |
| `409` | `actor_not_due` | 尚未到该角色的思考 tick |
| `200` Job / `409` / `500` | `proposal_outcome_unknown` | Proposal 持久化结果未决；保留原 Attempt 且不得 fallback，使用相同身份对账 |
| `422` | `no_safe_action` | 边界触发但游戏没提供安全动作 |
| `413` | `body_too_large` | 请求在 Snapshot 校验前已超过配置的请求正文上限 |
| `413` | `snapshot_too_large` | 已解码的完整 compact Snapshot 超过 16 MiB inline 上限；不会截断任何内容 |
| `429` | `jobs_queue_full` / `jobs_capacity` | 异步队列或保留区已满 |
| `429` | `generation_queue_full` / `generation_capacity` | 生成队列或保留区已满 |
| `503` | `jobs_unavailable` / `jobs_closed` | Proposal Job 服务未启用或正在关闭 |
| `503` | `generation_unavailable` / `generation_closed` | 生成服务未启用或正在关闭 |

服务从不把事件 payload、Token、内部路径或模型响应原文放入错误消息。

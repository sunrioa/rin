# Rin Protocol v1

[English](protocol-v1.md) | [简体中文](protocol-v1.zh-CN.md)

本文定义 Rin 与游戏自有适配器之间稳定的 HTTP 与状态契约。

## Envelope 封装

请求使用 `Content-Type: application/json`，默认最大 32 MiB，以容纳完整存档快照；各类数组和字段仍有更小的结构上限。成功响应：

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
  "features": ["memory-archive-v1", "belief-conflicts-v1"],
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
- `arbitration-v1`：启用 world revision、多角色仲裁与原子批量 commit。

省略该字段的旧 Session 保持 v0.4 行为，重放 hash 和 JSON 形状不变。

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

只有 `observer_ids` 中的角色获得这段记忆。Fact 若带 `visibility`，只写入名单中的观察者，避免 NPC 知道未见过的事情。

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
- `status: pending`：必须 commit 才生效。
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

取消：

`DELETE /v1/jobs/{job_id}`

相同 Session 和 `request_id` 的重复提交返回同一个 Job。若 payload 不同则返回 `request_id_conflict`。Job 队列有界，满载时返回 `429 jobs_queue_full`。

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

同一 `request_id` 与相同 payload 返回同一 Job；相同语义但不同 ID 可以命中短期缓存。Generation 任务不写入事件日志，游戏应先按自己的内容契约验证结果，再决定是否接受到 Canon。供应商失败不会自动生成替代剧情，调用方必须提供离线内容。

## 提交结果

`POST /v1/action/commit`

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

接受提案会记录行动结果、更新调度、标记记忆被召回，并让关联目标自动前进 1。拒绝提案不会修改角色记忆、事实和目标。

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

结果以目标优先级、tick、actor ID、proposal ID 确定性排序，给出 `selected` 或 `deferred`。仲裁是建议记录，不直接改变游戏世界。游戏应用选中动作后，可用 `POST /v1/action/commit-batch` 一次提交每个角色最多一个结果；任何一项失效都会拒绝整个批次，不产生部分修改。

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
  "snapshot": {"protocol_version":"rin.protocol/v1","state_hash":"...","state":{}}
}
```

Restore 拒绝 hash 错误、Session ID 不同或 Binding 不同的快照，并清空 pending Proposal。

当游戏反复载入同一存档时，Restore `request_id` 应同时绑定目标 Snapshot hash 和 Sidecar 当前 head hash。这样一次网络重试仍然幂等，而从后来状态再次读档会产生新的 Restore 事件并真正回退。

## Timeline 与 Replay

`POST /v1/session/timeline` 返回分页的事件类型、revision、hash、请求 ID、角色/实体 ID 和状态，不返回 Observation summary/quote、Commit outcome、Prompt 或模型正文：

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1","after_revision":0,"limit":50}
```

响应中的 `next_after_revision` 可用于下一页，`limit` 为 1–256。

`POST /v1/session/replay` 使用正常 reducer 和 hash-chain 校验重建指定 revision，并返回不落盘的 Snapshot：

```json
{"protocol_version":"rin.protocol/v1","session_id":"playthrough-1","revision":42}
```

Replay 会包含该 revision 已存在的角色记忆和剧情状态，因此沿用 Session API 的鉴权边界，不能当作脱敏日志接口。

## 常见错误

| HTTP | 错误码 | 含义 |
| --- | --- | --- |
| `400` | `invalid_json` / `invalid_request` | JSON 或字段契约错误 |
| `401` | `unauthorized` | Bearer Token 缺失或错误 |
| `404` | `session_not_found` / `unknown_actor` | 实体不存在 |
| `404` | `revision_not_found` | Replay revision 不存在 |
| `409` | `state_changed` / `proposal_stale` | 基础状态已改变 |
| `409` | `actor_not_due` | 尚未到该角色的思考 tick |
| `422` | `no_safe_action` | 边界触发但游戏没提供安全动作 |
| `413` | `body_too_large` | 请求超过大小限制 |
| `429` | `jobs_queue_full` / `jobs_capacity` | 异步队列或保留区已满 |
| `429` | `generation_queue_full` / `generation_capacity` | 生成队列或保留区已满 |
| `503` | `jobs_unavailable` / `jobs_closed` | Proposal Job 服务未启用或正在关闭 |
| `503` | `generation_unavailable` / `generation_closed` | 生成服务未启用或正在关闭 |

服务从不把事件 payload、Token、内部路径或模型响应原文放入错误消息。

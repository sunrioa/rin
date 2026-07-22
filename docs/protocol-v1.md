# Rin Protocol v1

## Envelope

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

## Create session

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
  "actors": [
    {
      "id": "npc.mira",
      "kind": "npc",
      "display_name": "Mira",
      "traits": ["curious", "careful"],
      "boundaries": [
        {
          "id": "boundary.privacy",
          "description": "Do not reveal private letters.",
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

## Observe

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

## Propose

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

在线模型不建议由游戏主线程直接调用本端点，应使用异步 Job API。

## Async proposal jobs

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

## Commit

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

## Scheduler

`POST /v1/scheduler/due`

```json
{
  "protocol_version": "rin.protocol/v1",
  "session_id": "playthrough-1",
  "tick": 24,
  "limit": 16
}
```

按 `next_think_tick` 和 actor ID 稳定排序，便于回合制、区域制和时间片游戏使用。

## Snapshot and restore

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

## Common errors

| HTTP | Code | Meaning |
| --- | --- | --- |
| `400` | `invalid_json` / `invalid_request` | JSON 或字段契约错误 |
| `401` | `unauthorized` | Bearer Token 缺失或错误 |
| `404` | `session_not_found` / `unknown_actor` | 实体不存在 |
| `409` | `state_changed` / `proposal_stale` | 基础状态已改变 |
| `409` | `actor_not_due` | 尚未到该角色的思考 tick |
| `422` | `no_safe_action` | 边界触发但游戏没提供安全动作 |
| `413` | `body_too_large` | 请求超过大小限制 |
| `429` | `jobs_queue_full` / `jobs_capacity` | 异步队列或保留区已满 |
| `503` | `jobs_unavailable` / `jobs_closed` | Job 服务未启用或正在关闭 |

服务从不把事件 payload、Token、内部路径或模型响应原文放入错误消息。

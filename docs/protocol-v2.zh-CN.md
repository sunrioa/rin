# Rin 协议 v2

[English](protocol-v2.md) | [简体中文]

Rin `0.7.0` 是 **Preview** 版本，Wire 标识为 `rin.protocol/v2`。v2 是有意的
破坏性升级：v1 请求会被拒绝，不会进入兼容分支。

权威机器契约是 [`api/openapi.json`](../api/openapi.json)。本文补充 JSON Schema
无法表达的跨引擎语义。

## 权威边界

Rin 负责提出建议，游戏 Host 负责决定与执行。

- Host 创建 Observation、Epoch、Decision Window、Action Offer 和稳定 Operation
  身份。
- Policy 只能选择当前 Decision Window 中由游戏完整编写的 Offer，不能发明
  Capability、Target 或 Argument。
- Proposal 被接受仍只是建议；只有权威 Host 能启动或完成 Operation。
- Host 通过 `/v2/action/report` 或 `/v2/action/report-batch` 回报实际决定与
  生命周期。
- Rin 不执行 Shell、游戏控制台命令、模型生成代码或模型任意指定的方法名。

## 传输与 Envelope

除健康探针外，每个 JSON 请求都包含：

```json
{"protocol_version":"rin.protocol/v2"}
```

成功响应使用 `{"ok":true,"data":...}`；错误使用
`{"ok":false,"error":{"code":"...","message":"...","field":"..."}}`。
客户端必须拒绝重定向、限制请求与响应大小、对非 Loopback 地址强制 HTTPS，
并且不得把 Bearer Token 写进游戏存档。

标识符由小写 ASCII 段和 `.`、`_`、`-` 分隔符组成。跨 SDK 的整数必须位于
JSON 安全范围 `0..9007199254740991`。所有 Wire 与持久化边界的 JSON Object
Member Name 都必须唯一。

## 创建 Session

`POST /v2/session/create` 把一个持久 Playthrough 绑定到不可变内容：

```json
{
  "protocol_version": "rin.protocol/v2",
  "request_id": "create.playthrough-1",
  "session_id": "playthrough-1",
  "binding": {
    "game_id": "example.game",
    "content_id": "base",
    "content_version": "1.0.0",
    "content_hash": "content-build-42"
  },
  "features": [],
  "actors": [{
    "id": "npc.mira",
    "kind": "npc",
    "display_name": "Mira",
    "think_every_ticks": 5,
    "enabled": true
  }]
}
```

v2 动作生命周期无需 Feature Opt-in。可选 Feature 仅提供增量能力，必须通过
`/health` 协商。同一请求可幂等重试；用同一 `request_id` 发送不同 Payload
会报错。

## Epoch 与 Host 时间

`Epoch` 防止为旧世界世代捕获的动作应用到新世界：

```json
{
  "session_id": "playthrough-1",
  "world_id": "overworld",
  "host": 1,
  "world": 4,
  "timeline": 2
}
```

- 权威进程重新建立时递增 `host`；
- Scene、Level、Shard 或权威世界加载后递增 `world`；
- 回滚、存档分支或加载较旧状态后递增 `timeline`。

三个 Generation 都必须是正数且位于 JSON 安全范围。Epoch 的 `session_id`
必须等于外层 Request 与 Session State；即使其他字段有效，来自另一 Session
的嵌套 Epoch 也会被拒绝。

`Timepoint` 形如 `{ "clock": "event|step|realtime", "value": N }`。
Realtime 值是 Unix 毫秒；Event 与 Step 值是 Host 单调计数器。渲染帧不是
权威时间。

## Observation

`POST /v2/session/observe` 记录 Host 已观察到的事件。每条 Observation 包含
`epoch` 和正数、单调递增的 `observation_seq`。大型图片、音频、遥测与 Replay
片段应存到外部，以不可变 Artifact 引用，不应直接塞进事件日志。

可选 `payload` 是 `HostValidatedPayload`，不是未受信模型输出。Schema 引用是
已认证 Host 作出的断言：Adapter 必须在发送请求前，按该精确 Schema 与 Digest
校验 `data`。Rin 只校验引用、字节上限和严格 JSON envelope，不会解析游戏自有
Schema。Go Adapter 应使用 `protocol.NewHostValidatedPayload` 构造；其他语言
Adapter 必须执行等价的本地校验。

## Decision Window 与 Offer

Proposal Request 把一个 Actor 绑定到一次 Host 创建的决策机会：

```json
{
  "protocol_version": "rin.protocol/v2",
  "session_id": "playthrough-1",
  "request_id": "propose.turn-42",
  "actor_id": "npc.mira",
  "tick": 42,
  "intent": "Respond to the player.",
  "decision_window": {
    "id": "window.turn-42",
    "mode": "sequential",
    "epoch": {
      "session_id": "playthrough-1",
      "world_id": "overworld",
      "host": 1,
      "world": 4,
      "timeline": 2
    },
    "observation_seq": 81,
    "opened_at": {"clock":"step","value":420},
    "deadline": {"clock":"step","value":430},
    "actor_ids": ["npc.mira"]
  },
  "offers": [{
    "offer_id": "offer.greet",
    "decision_window_id": "window.turn-42",
    "actor_id": "npc.mira",
    "capability": {"id":"dialogue.say","version":"1.0.0"},
    "descriptor_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "description": "Say the authored greeting.",
    "arguments": {"text":"Welcome, traveler."},
    "expected_epoch": {
      "session_id": "playthrough-1",
      "world_id": "overworld",
      "host": 1,
      "world": 4,
      "timeline": 2
    },
    "observation_seq": 81,
    "deadline": {"clock":"step","value":430}
  }]
}
```

Offer 是完整绑定的授权 Envelope。`descriptor_digest` 标识经过校验的确切
Capability Descriptor；Target 是只有所属 Adapter 才能解析的 `HostRef`。
只有 Epoch、Observation Sequence、Deadline、Actor、Window、Digest 与所选
Offer 都仍匹配权威游戏状态时，Proposal 才有效。

Decision Mode 包括 `sequential`、`simultaneous`、`asynchronous`。会修改共享
状态的 Simultaneous 结果应先 Arbitration，再作为一个 Batch 回报。

## 动作生命周期

Host 分配稳定 `operation_id`，并回报以下一种状态：

- `rejected`：不能携带 Invocation、Run、Outcome、Fact 或 Goal Update；
- `accepted` 且带 Invocation，以及 `queued` 或 `running` Run；
- `accepted` 且带终态 Run 和匹配 Outcome。

Run Status 为 `queued`、`running`、`succeeded`、`failed`、`cancelled`、
`interrupted`、`stale`、`outcome-unknown`。终态回报必须带有 Operation、
Status 与 Expected Epoch 均匹配的 Outcome。

```json
{
  "protocol_version": "rin.protocol/v2",
  "session_id": "playthrough-1",
  "request_id": "report.operation-42",
  "tick": 43,
  "report": {
    "proposal_id": "proposal.turn-42",
    "event_id": "action.operation-42",
    "decision": "accepted",
    "summary": "Mira greeted the player.",
    "invocation": {
      "operation_id": "operation-42",
      "offer_id": "offer.greet",
      "decision_window_id": "window.turn-42",
      "actor_id": "npc.mira",
      "capability": {"id":"dialogue.say","version":"1.0.0"},
      "descriptor_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "arguments": {"text":"Welcome, traveler."},
      "expected_epoch": {
        "session_id": "playthrough-1",
        "world_id": "overworld",
        "host": 1,
        "world": 4,
        "timeline": 2
      },
      "observation_seq": 81,
      "deadline": {"clock":"step","value":430}
    },
    "run": {
      "operation_id": "operation-42",
      "status": "succeeded",
      "progress_seq": 1,
      "progress": 100,
      "updated_at": {"clock":"step","value":421}
    },
    "outcome": {
      "operation_id": "operation-42",
      "status": "succeeded",
      "summary": "The line was displayed.",
      "epoch": {
        "session_id": "playthrough-1",
        "world_id": "overworld",
        "host": 1,
        "world": 4,
        "timeline": 2
      },
      "world_seq": 82,
      "occurred_at": {"clock":"step","value":421}
    }
  }
}
```

Report 描述已经发生的效果。重放已确认 Report 时不得再次应用游戏动作。

## 持久接入算法

会修改世界的 Host 应持久化：

1. 第一次网络请求之前保存完整 Pending Turn；
2. 第一次轮询之前保存返回的 Job ID；
3. 若声明 Transactional Durability，在一个游戏事务内保存 Operation Marker
   与确切 Report；
4. Rin 确认确切 Report 前一直保留 Outcome Outbox。

重启后先 Drain Outbox，再用完全相同的请求身份与 Payload 恢复 Pending Turn。
Idempotent Host 可以重试 `Execute(operation_id)`；Advisory Host 不得宣称这能
关闭 Apply 后、持久化前的崩溃窗口。

Proposal Job 只是进程内优化。Sidecar 重启后若已保存 Job 不存在，应重新提交
确切的持久 Proposal Request，不得生成新请求身份。

## 存储与长时间 Session

通过 `/v2/session/stats` 监控 Event Log、Snapshot、Checkpoint 与 Index 字节数。
配置每 Session Soft/Hard Limit，定期 Snapshot，并归档已结束 Lineage。大于
Inline Snapshot 限制的 Lineage 使用有界 NDJSON Export/Import。

Rin 对 Actor 细节与派生 Index 设定上限；Append-only Event History 仍是重建
来源。即使已压缩实体离开当前 State，Identifier History 仍会阻止跨 Kind
复用身份。

## 兼容规则

本版本唯一支持的 Wire Contract 是 `rin.protocol/v2`。Preview 期间 Response
可能新增字段，因此 SDK 应忽略未知响应字段；Request 仍保持严格，未知请求字段
会被拒绝，以尽早暴露接入错误。

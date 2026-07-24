# RPG 事件约定

[English](rpg-events.md) | [简体中文](rpg-events.zh-CN.md)

这些约定让 RPG、模拟、战术游戏和开放区域 NPC 系统使用 Rin，同时不把
世界权威交给 Agent。

## 身份与 Tick

- 将 `session_id` 绑定到一个周目和一个内容/Mod 指纹。
- 使用 `npc.harbor.blacksmith` 这类稳定 Actor ID，不要把显示名称当作身份。
- 在游戏拥有的时钟上推进 `tick`，例如回合、分钟、日程槽或模拟步；不要用
  渲染帧。
- 根据玩法重要性设置 `think_every_ticks`。远处或未加载的 NPC 应休眠，
  不应轮询模型。
- 传送、重新加载、回滚或任务状态重写都属于新的 Observation/revision，
  会让基于旧 head hash 的 Proposal 失效。

## 区域与可见性

区域成员关系由游戏决定。推荐的 Observation kind 和 tag：

| 事件 | `kind` | 示例 tag |
| --- | --- | --- |
| Actor 进入已加载区域 | `region-enter` | `region.harbor`, `visibility.direct` |
| Actor 离开已加载区域 | `region-exit` | `region.harbor` |
| 可见的世界动作 | `world-action` | `visibility.direct`, `combat` |
| 听见但未看见的事件 | `sound` | `visibility.heard`, `region.market` |
| 对白 | `dialogue` | `conversation`, `speaker.player` |
| 私密发现 | `discovery` | `visibility.private`, `quest.relic` |

只有位于 `observer_ids` 的 Actor 才会获得记忆。距离近并不等于可观察；
构造列表前应考虑墙壁、潜行、失聪、语言、无线电频道、过场和暂时失能。

Fact 使用自己的 `visibility` 白名单。这样，听到声音的角色不会同时知道
隐藏攻击者的身份。不要发送带有“hidden”标签的删减秘密文本；在事实真正
可观察前应完全省略。

## 任务与 Quest

任务状态保留在游戏中。Rin 可以记住有限事实，例如：

```json
{
  "subject_id": "quest.repair-bridge",
  "predicate": "stage",
  "object": "materials-delivered",
  "visibility": ["npc.harbor.foreman"],
  "confidence": 100
}
```

任务变化时发送 Observation，然后只提供当前阶段合法的动作。
`offer-next-step` 这类 Proposal 只是对白意图；是否推进任务、发放奖励或
修改背包仍由游戏决定。

Rin 会根据外层 Observation 或 Commit 的 `event_id` 生成存储后 Fact 的
`source_event_id`，调用方应在请求中省略该字段。传闻应使用较低置信度。
两个角色意见冲突时保留两条 Observation，不要悄悄把其中一条提升为世界真相。

## 候选动作

动作应描述游戏能够验证和应用的能力：

- `dialogue`：说话、询问、警告、议价、拒绝；
- `move`：前往当前可达目标；
- `interact`：使用可用物体或工作台；
- `combat`：防御、撤退、使用已装备能力；
- `social`：邀请、解散、请求帮助；
- `wait`、`redirect`、`refuse`：安全且不升级冲突的结果。

把目标 ID 和有界参数放进动作 spec。不要提供当前导航网格、任务阶段、
冷却、背包、同意状态或战斗规则会拒绝的动作。Rin 的白名单是安全边界，
不只是 Prompt 提示。

高影响动作应提供 `request-trade` 或 `attempt-attack` 这类意图；权威游戏
系统在 Proposal 验证后计算价格、命中、伤害、所有权和后果。

## 应用与提交

以下顺序只适用于显式启用 `outcome-reporting-v1` 的 Session；旧 Session
继续使用原有 Commit 语义。

1. 若目标移动、死亡、离开可见范围、改变阵营或失去所需资源，拒绝过期提案。
2. 通过正常玩法系统应用选定动作，或决定拒绝。
3. 在同一权威事务中把实际结果写入游戏自己的 Outcome Outbox。
4. 从 Outbox Commit 实际结果；状态已经前进不会使已发生结果失效。
5. 只向确实感知结果的 Actor 发送后续 Observation。

被拒绝的 Proposal 仍是有价值的审计历史。若动作作为角色意图仍然有效，
只是被游戏规则拒绝，应以 `accepted=false` Commit。不要 Commit 适配器
本地的 `offline.*` Proposal；之后通过 `observe` 报告它们的实际结果。

Commit 超时或暂时失败时只重报同一 Outbox 项，不得再次执行动作。完整规则见
[动作结果记账](outcome-reporting.zh-CN.md)。

## 边界与玩家安全

模型侧意图永远不能覆盖本地的同意、骚扰、购买、不可逆任务选择、PvP、
账户操作或用户生成内容规则。任何可能触发边界的请求都应包含安全的
`refuse`、`redirect` 或 `wait` 动作。

NPC 可以拒绝、误解、延迟或追求小目标，但不能创建新的合法目标、泄露
未观察事实、消费货币或重写其他 Actor 的状态。

## 扩展到大量 Actor

- 在模拟 tick 或区域激活时查询 `/v1/scheduler/due`，不要每帧查询。
- 只为已加载且相关的 Actor 提交 Job，并在游戏侧和 Rin 侧都限制并发。
- 若所有列出的 Observer 都感知了同一结果，把世界事件合成一条简洁
  Observation。
- 重要具名 NPC 使用较高频率；人群使用确定性策略。
- 在游戏存档边界创建 Snapshot；Restore 必须携带来自运行中可信内容 manifest
  的 `expected_binding`，并与 Snapshot 及任何 existing target Session 一致。
- Snapshot 是按事件日志保护的可信、不透明状态；其 SHA-256 canonical checksum
  只能发现意外损坏，不能证明来源真实性或阻止能重算 checksum 的修改者。
  Inline compact JSON 上限为 16 MiB；服务端请求与随附客户端响应默认上限为
  32 MiB。`413 snapshot_too_large` 绝不截断 lineage，需等待计划中的 Step 5
  streaming transport。

这样，模型成本与有意义的决定数量成正比，而不是与人口或帧率成正比。

# Signal 收件箱

[English](signals.md) | [简体中文](signals.zh-CN.md)

Signal 是游戏 Host 对“某件事值得角色注意”的短期提示。它不是动作、事实写入、权限或
执行结果。具体事件名由 Adapter 使用命名空间定义，例如 `minecraft.player.death`。

## 流程

1. Adapter 合并高频游戏事件，并随 Actor Publication 发布最新 Observation。
2. Host 使用同一 Lease、Epoch 和 Observation Sequence 发布 Signal。
3. Rin 完成 ID 去重、同类冷却、过期和有界容量处理。
4. internal Authority 下，启用了匹配 Initiative Trigger 的 Persona 可被唤醒并创建普通任务。
5. external Authority 下，Rin 不调用内部模型；外部 Agent 通过 MCP
   `list_actor_signals` / `wait_actor_signals` 读取提示。
6. 后续动作仍必须创建 ActionRequest，并经过 Controller、Policy、Operation 和 Host Outcome。

Signal 默认关闭。Host 可按 Actor 配置 `enabled`、`cooldown_millis` 和 `max_pending`。
收件箱只保存在 daemon 进程内，重启后清空，避免旧 Epoch 的提醒进入新时间线。

## Host API

契约来源是 [`../api/signal-openapi.json`](../api/signal-openapi.json)：

- `POST /signals/v1/host/settings`
- `POST /signals/v1/host/publish`
- `POST /signals/v1/list`
- `POST /signals/v1/wait`

Java Adapter 可直接使用 `HostControlSession.configureSignals` 与
`HostControlSession.publishSignal`。Host 发布必须匹配当前 Actor 的 Epoch 和 Observation；
Rin 分配 `received_at_unix_millis` 与 `cursor`。

## 边界

- Adapter 负责事件采集和合并；Rin Core 不维护跨游戏情绪词典。
- Summary 只能陈述可观察事件或带不确定措辞的假设，不能伪装成权威 Outcome。
- 被禁用、重复、处于冷却或容量已满的 Signal 会在 PublishResult 中返回原因，不创建任务。
- Signal 不持久化、不确认消费，也不增加 `claim/ack` 状态机。

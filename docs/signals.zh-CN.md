# Signal 收件箱

[English](signals.md) | [简体中文](signals.zh-CN.md)

Signal 是游戏 Host 对“某件事值得角色注意”的短期提示。它不是动作、事实写入、权限或
执行结果。具体事件名由 Adapter 使用命名空间定义，例如 `minecraft.player.death`。

## 流程

1. Adapter 合并高频游戏事件，并随 Actor Publication 发布最新 Observation。
2. Host 使用同一 Lease、Epoch 和 Observation Sequence 发布 Signal。
3. Rin 完成 ID 去重、同类冷却、过期和有界容量处理。
4. internal Authority 下，匹配 Initiative Trigger 的事件先进入 Actor 当前任务的协调流程。
5. external Authority 下，Rin 不调用内部模型；外部 Agent 通过 MCP
   `list_actor_signals` / `wait_actor_signals` 读取提示。
6. 后续动作仍必须创建 ActionRequest，并经过 Controller、Policy、Operation 和 Host Outcome。

Signal 默认关闭。Host 可按 Actor 配置 `enabled`、`cooldown_millis` 和 `max_pending`。
收件箱只保存在 daemon 进程内，重启后清空，避免旧 Epoch 的提醒进入新时间线。

## Actor 任务协调

- 未完成任务接收有界、非可信的 Signal 上下文。等待新观察的任务会被唤醒；等待 Operation
  和人工暂停仍保留原有条件，不会被信号跳过。
- 同类上下文合并为最新摘要。每个任务最多保存 8 种待处理事件及 64 个近期 Signal ID；
  ID 去重记录可恢复。成功完成一轮模型决策后消费上下文，过期或不同 Epoch 的内容不会进入模型。
- Actor 空闲时才创建主动任务。任务创建按 Host/World/Actor 串行，即使并发调用方使用同一
  Controller ID，也不会创建重叠的未完成任务。Persona 的 `cooldown_millis` 还限制普通主动任务的创建频率。
- `initiative_policy.preempt_triggers` 是显式事件类型白名单，默认空。匹配的高优先级信号
  可以请求取消普通主动任务，但必须等其 Host 操作确定停止后才启动替代任务。调用方创建的
  任务及其他高优先级主动任务只接收上下文；未解决的 `outcome-unknown` 阻止自动替代。
- 同一 Epoch 内，已接收的旧序号信号仍可触发重新判断。Runtime 必须读取最新 Host 观察，
  旧信号不能提供可复用的动作绑定；不同 Epoch 的事件会被丢弃。

收件箱记录 `delivery.status`（`started`、`attached`、`merged`、`retry`、`dropped`）、
原因、任务 ID、尝试次数及重试时间。暂时性失败按 1、2、4、8 秒退避，之后保持 8 秒，最多
32 次且不超过有效期；其他新信号可以继续处理。关闭收件箱会丢弃待投递事件。查看处理状态变化时
从游标零重新列举；普通游标等待仍追踪新发布的 Signal。

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
- 收件箱投递诊断仅在进程内保留，并随 Signal 过期；已经写入 Task 的上下文可持久恢复。
  不增加公开的 `claim/ack` 权限，Host 也不能提交 `delivery` 状态。

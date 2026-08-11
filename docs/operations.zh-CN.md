# Operation 与 Gameplay Policy

[English](operations.md) | [简体中文](operations.zh-CN.md)

Control Plane 把一次 `ActionRequest` 变成可持久、可等待、可取消和可对账的
Operation。Gameplay Policy 在 Operation 入队前检查 Host 绑定出的实际 Effect。

## 唯一修改入口

`SubmitAction` 是 V2 唯一的 Controller 世界修改入口：

1. 校验 Principal、Scope、Actor 可见性和独占 Controller Lease；
2. 校验 Request 的 Epoch、Observation Sequence、Capability Digest 和幂等身份；
3. 请求 Host 在权威线程生成 `BoundAction` 与 Effect Preview；
4. 再次确认 Authority Revision、Lease、Epoch 和 Observation 未变化；
5. 用 Gameplay Policy 评估所有 Effect；
6. 持久化 Operation 后才允许 Host 领取。

内部 Agent、MCP 和语言 SDK 最终都调用这条入口。游戏命令或 UI 若要触发同类自主
行动，也应进入相同的 Adapter 执行服务，不能维护第二套权限语义。

## Policy

Policy 是确定性的，不调用模型或网络。它只匹配 Host 编写的标准 Effect 字段和
可信 Context，不读取模型理由或 Adapter Attributes 中的任意文字。

### 安全内核

以下情况始终拒绝，配置不能覆盖：

- arbitrary code、file access、native call、authority forgery、secret exposure；
- 未注册的 Effect Kind 或 Scope；
- `ownership=unknown`；
- 无效 Profile、Epoch 或 Principal。

### Profile

| Profile | 默认行为 |
| --- | --- |
| `guarded` | 允许低风险读取、交流和可逆自有操作；高风险拒绝；其余要求确认 |
| `survival` | 允许已知普通生存 Effect；玩家/共享/系统资产或高风险要求确认；critical 拒绝 |
| `open` | 允许已知非 critical Effect；critical 仍要求确认 |
| `privileged-custom` | 与 open 相同的内核下，由显式 Rule 和 Budget 细化 |

Profile 只提供默认值。Rule 可在 server、world、owner、actor、task 五层按 Kind、
Operation、Ownership、Scope、Tag、Risk 和 Reversible 匹配 allow、deny 或
require_confirmation。优先级冲突必须得到确定结果。

### Budget

Budget 按相同层级限制动作数和数量，并可绑定 Host Clock Window。Allow 决策先保留
Budget；只有 Host 接受并实际进入执行链后才提交 Usage，拒绝或持久化失败会释放。
Policy State 保存 Usage 与未完成 Reservation，但不保存可重用确认。

## 确认

`require_confirmation` 生成单次 Challenge，绑定：

- Controller、Actor、Principal；
- Effect Digest、Policy Revision 和 Epoch；
- Host Clock 到期时间。

确认时不会重新绑定或悄悄修改原 Action。Control Plane 先向 Host 获取新 Snapshot，
确认 Epoch、Observation、Lease 和急停均未变化，再消费 Challenge 并重新评估同一个
`BoundAction`。任一条件变化都会变为 `stale` 或再次要求确认。

## 状态机

```mermaid
stateDiagram-v2
    [*] --> awaiting_confirmation: policy requires confirmation
    [*] --> queued: policy allows
    [*] --> rejected: policy denies
    awaiting_confirmation --> queued: valid confirmation
    awaiting_confirmation --> stale: binding or authority changed
    queued --> delivered: Host polls
    delivered --> accepted: Host ACK
    delivered --> rejected: Host rejects
    accepted --> running: Host reports progress
    accepted --> succeeded: immediate Outcome
    running --> succeeded: successful Outcome
    accepted --> failed: failed Outcome
    running --> failed: failed Outcome
    queued --> stale: Host/epoch expires before delivery
    accepted --> outcome_unknown: restart or reconciliation gap
    running --> outcome_unknown: result cannot yet be proven
    outcome_unknown --> succeeded: late authoritative Outcome
    outcome_unknown --> failed: late authoritative Outcome
    queued --> cancelled: cancellation confirmed
    accepted --> interrupted: Host interruption
```

实际终态还包括 `cancelled`、`interrupted`、`stale` 和 `rejected`。状态转换只能前进；
`progress_seq` 必须单调，迟到或倒退的 Run 被拒绝。

## 执行证明

Operation View 显式包含：

- `terminal`：是否已经稳定终止；
- `execution_confirmed`：是否存在 Host 权威成功 Outcome；
- `reconciliation_pending`：是否仍等待 Host 对账；
- `delivery_attempts`：Host 实际领取次数；
- `run`、`outcome`、`output` 和拒绝原因。

只有 `status=succeeded`、存在合法 Host Outcome 且
`execution_confirmed=true` 才能向玩家报告行动已经完成。

`queued` 仅表示已耐久入队；`accepted` 仅表示 Host 接受；`running` 仅表示执行中；
`changed=false` 仅表示长轮询期间没有新版本。`stale` 且
`delivery_attempts=0` 明确表示 Host 从未领取请求。

## 幂等与等待

同一 Principal 和 `idempotency_key` 的完全相同 Action 返回同一 Operation。相同
Key 配不同 Payload 会冲突。网络超时后应查询或精确重试原身份，不能换 Key 重发。

`wait_operation` 使用不透明 Cursor，单次最长等待 25 秒。客户端复制 Cursor 即可，
不应解析其格式。超时不会取消 Operation。

## Host 投递与恢复

Host 通过 Lease 长轮询：

1. `poll` 收到 Binding Gateway、Operation 或取消请求；
2. 用 Gateway ID 幂等返回 Binding/Snapshot；
3. 对 Operation ID 幂等 `ack`；
4. 可选上报 `run`；
5. 从游戏结果和持久 Outbox 上报唯一 `outcome`。

Host 在 ACK 前断线时，无法证明绑定仍适用的请求会变为 `stale`。ACK 后断线时，
同一 Operation 可以重投；Adapter 必须按其 Durability Profile 去重。已经看到执行
迹象但缺少结果时进入 `outcome-unknown`，允许 Host 后续提交权威 Outcome。

Control 状态使用单写者锁和原子文件替换。Host Read Model 与 Lease 由 Host 重连后
重新发布；Operation 身份和已持久终态不会因 Daemon 重启而改变。

## 取消与急停

取消只请求停止，不能撤销已经发生的 Effect。Capability 声明 unsupported、
cooperative 或 preemptive 取消语义；最终状态由 Host 报告。

Emergency Stop 是 Actor 级、Owner 控制的持久安全闩：

- 阻止内部和外部来源提交新动作；
- 请求取消所有未完成 Operation；
- 不自动恢复已执行世界修改；
- 解除后仍需重新获取有效 Controller Lease 和新 Observation。

## Macro

Macro 是可产生子 Operation 的 Capability，不是任意计划脚本。Parent 必须已经被 Host
接受或运行，父子共享 Actor、Controller Lease、Principal 和非空 `task_id`。每个
Child 仍独立执行 Binding、Policy、Operation 和 Outcome，最多 1024 个子 Operation。

这样模型可以组合采集、移动、合成和建造等连续任务，同时保留逐步授权、预算、取消
和故障定位。

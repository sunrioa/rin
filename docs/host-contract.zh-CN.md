# Host V2 契约

[English](host-contract.md) | [简体中文](host-contract.zh-CN.md)

`rin.host/v2` 是 Rin 与权威游戏 Adapter 之间的引擎无关契约。它统一可验证的
身份、观察、能力、意图、效果和结果，不统一游戏世界模型。

## Host Manifest

`HostManifest` 声明 Adapter 的静态事实：

- Adapter、引擎、Runtime 与平台身份；
- `standalone`、`server` 或 `client-advisory` 权威模式；
- loopback、dedicated、remote HTTPS、embedded offline 或 computer-control 部署描述；
- event、step、realtime 时钟和 sequential、simultaneous、asynchronous 决策方式；
- 最大 Actor 并发和实际 Durability 保证。

Durability 只描述崩溃与重试能力：

| Profile | 保证 |
| --- | --- |
| `advisory` | 不承诺世界修改的幂等恢复 |
| `idempotent-action` | 相同 Operation 可重复投递而不重复效果 |
| `transactional-action` | 世界效果和结果记录由同一游戏事务提交 |

声明高等级不能创造保证；真实 Adapter 必须用故障注入和游戏存档机制证明它。

## Epoch 与时钟

`Epoch` 由稳定的 Session/World ID 和三个正整数 Generation 组成：

- `host`：权威 Host 实例更换；
- `world`：场景、维度、地图或世界重新加载；
- `timeline`：读档、回滚或分支时间线。

Generation 不是渲染帧或 Tick。`Timepoint` 才表示 Host 的 event、step 或 realtime
时钟。Action、Binding、Lease 和 Outcome 使用 Epoch 隔离旧时间线；时钟用于
Deadline、Execution Budget 和确认过期。

## Observation

`ObservationEnvelope` 是 Host 编写的有界快照，包含：

- Host、World、Actor、Epoch、单调 Sequence 和观察时间；
- 由 `SchemaRef` 标识的游戏专属 Payload；
- 标准化的 `facts`、`resources` 和无路径的 `artifacts`；
- 可选分页 Continuation Token。

`ObservationFact` 适合生命值、姿态、关系状态等标量事实。`ObservationResource`
额外声明 Kind、Tag、所有权、Scope、数量和 Host 验证的 Attributes。Artifact 只
携带 ID、媒体类型、大小和 SHA-256，不携带文件路径或任意下载 URL。

`HostRef` 是不透明引用。Controller 可以从 Observation 复制它，但不能构造或解析
其 `key`；只有所属 Adapter 可以在权威线程解析。标记为 `ephemeral` 的引用不得
跨 Epoch 或写入长期状态。

## Capability

`CapabilitySpec` 描述一种动作类型：

- 精确的命名空间 ID 与语义版本；
- 封闭的 Input、Output 和 Effect Attribute JSON Schema；
- `atomic` 或 `macro`；
- immediate、queued 或 long-running 执行方式；
- unsupported、cooperative 或 preemptive 取消方式；
- 风险下限、所需 Scope、Durability 和 Host Clock Execution Budget；
- 输入、输出、Effect 数量上限及是否产生子 Operation；
- 对规范化字段计算的不可变 Digest。

Discovery 只说明 Host 实现了什么，不说明某次行动已获授权。当前 Actor 的 Authority、
Controller Lease、目标状态、Effect Policy 和 Adapter 本地规则仍会分别检查。

## Schema

Rin 使用自包含 JSON Schema 2020-12。Capability Schema 必须是封闭根 Object，
有严格大小限制，不加载外部引用。Rin 对规范化 Schema 计算 SHA-256，并把三个
Schema 和执行限制密封到 Capability Digest。

Controller 必须提交精确 Digest。Host 修改参数 Schema、风险、预算或执行语义时，
即使 Capability ID/Version 未变，旧请求也会失败。正式发布应同时提升语义版本。

## ActionRequest

Controller 唯一可编写的行动意图是 `ActionRequest`：

```text
request_id / idempotency_key
controller_id / actor_id / task_id
capability id + version + spec_digest
arguments / target_refs
expected_epoch / observation_sequence
```

参数必须符合 Capability Input Schema，目标必须来自可信 Observation。Request 不含
Effect、风险、所有权、授权结果或任意执行函数。

## Binding 与 Effect Preview

Adapter 在权威线程执行 `Bind`：

1. 获取当前 Snapshot；
2. 验证 Capability、Digest、Epoch 和 Observation Sequence；
3. 解析参数与 `HostRef`；
4. 返回规范化目标和有效期；
5. 根据真实游戏对象生成 Effect Preview；
6. Registry 密封不可变 `BoundAction`。

`Effect` 的标准字段包括：

- Kind 与 read/create/update/delete/transfer/consume/execute/communicate Operation；
- Subject/Target、Tag、所有权、Scope、数量和单位；
- 可逆性和 low/moderate/high/critical 风险；
- 通过 Capability Effect Schema 校验的游戏专属 Attributes。

这些字段必须由 Host 推导。Controller 文字、参数中的“safe=true”或模型自报风险
不能影响 Policy。

## 执行结果

`ActionRun` 上报 Operation ID、状态、单调 `progress_seq`、0-10000 的进度和 Host
时间。`ActionOutcome` 是唯一终态事实，绑定 Epoch、World Sequence、发生时间和
可选 Evidence。

取消是请求，不是回滚。`cancelled` 表示 Host 确认停止；`interrupted` 表示环境
中断；`outcome-unknown` 表示已经无法证明最终效果。成功 Outcome 还必须提供符合
Capability Output Schema 的结构化 Output。

## Adapter 接口

Go HostKit 的中立 `Adapter` 边界是：

```text
Manifest
Snapshot / Observe / ListCapabilities
Bind / Preview
Execute / Cancel / Verify
PolicyFacts
```

所有可能读取或修改游戏状态的方法都通过 `AuthorityDispatcher`。HostKit 可以帮助
做 Schema、Binding、最终 Epoch 和 Output 检查，但不能替具体游戏实现主线程切换、
导航、容器事务、资产识别或世界存档。

精确 Go 类型见 `host/`，HTTP 投影见
[`api/control-openapi.json`](../api/control-openapi.json)。

# 游戏适配器

[English](game-adapters.md) | [简体中文](game-adapters.zh-CN.md)

适配器把引擎生命周期事件转换为稳定的 Rin 协议，同时保持相同的权威边界：

1. 游戏只发送角色确实观察到的事件。
2. 游戏提供一组当前合法且数量有限的动作。
3. Rin 返回提案，但不会移动角色或修改世界。
4. 游戏执行自己的规则，并提交接受或拒绝后的真实结果。

适配器会在协议提案外增加两个本地字段：

- `committable=true`：提案来自当前 Sidecar 会话，游戏处理后可以把结果发送到
  `/v1/action/commit`；它不是 Rin 的执行授权。
- `committable=false`：当前没有可 Commit 的 Rin Proposal。只有
  `source=offline` 且返回 Proposal 时才能应用本地回退；canceled/error
  结果没有可执行动作。不能把 `offline.*` ID 发送给 Rin，Sidecar 恢复后
  应通过 `observe` 报告已经应用的 fallback。

提交、轮询或取消响应超时/丢失时，结果属于 outcome-unknown，而不是 offline；
应以相同 request/job 身份恢复，确认不存在在线 Proposal 前不得选择 fallback。

首次提交前应持久化 Proposal Attempt，其中包含字节等价的完整 Propose
Request、游戏 Operation/Sequence 身份，并在 `202` 返回后立即补上 Job ID。
后续交互必须恢复这条 Attempt，不能创建新 Request。只有在同一个权威事务中
应用或拒绝返回的 Proposal，并写入 Applied Marker 与 Outcome Outbox 后，才能
移除 Attempt。未决 Proposal Attempt 与未清空的 Outcome Outbox 都会阻止新
Turn。

新 Session 必须请求 `outcome-reporting-v1`，此时 Commit 才是已处理结果记账，
而不是旧版的提交前语义。游戏应在同一个权威事务中应用
或拒绝动作并写入本地 Outcome Outbox，再从 Outbox 向 Rin 回报。网络失败时只
使用相同 `request_id` 重报，绝不能再次应用动作。详细规则见
[动作结果记账](outcome-reporting.zh-CN.md)。

## Ren'Py

将以下文件复制到游戏的 `game/` 目录：

```text
adapters/renpy/rin_client.py
adapters/renpy/rin_bridge.rpy
```

客户端只使用 Python 标准库。需要显式启用：

```bash
export RIN_ENABLED=1
export RIN_BASE_URL="http://127.0.0.1:7374"
```

远程 TLS 反向代理需要设置 `RIN_TOKEN`；适配器拒绝非 loopback HTTP
以及无 Token 的远程端点。可选设置：

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `RIN_TIMEOUT_SECONDS` | `5` | 单次适配器 HTTP 请求 |
| `RIN_JOB_DEADLINE_SECONDS` | `25` | 异步提案总等待时间 |
| `RIN_POLL_INTERVAL_SECONDS` | `0.1` | Job 轮询间隔 |
| `RIN_LIVE_TEST_ENABLED` | `0` | 显式允许 Ren'Py 原生测试访问网络 |

在脚本中安排请求，继续渲染，再从 timer 或 call screen 消费结果：

```python
request_id = rin_schedule_proposal({
    "protocol_version": "rin.protocol/v1",
    "session_id": "playthrough-1",
    "request_id": "propose.scene-12.lin",
    "actor_id": "npc.lin",
    "tick": 12,
    "intent": "Choose how to answer.",
    "tags": ["conversation"],
    "candidate_actions": [
        {"id": "respond.honest", "kind": "dialogue", "description": "Answer honestly."},
        {"id": "respond.wait", "kind": "wait", "description": "Wait for now."},
    ],
}, fallback_action_id="respond.wait")
```

`rin_proposal_status(request_id)` 返回 `pending`、`ready`、`unresolved` 或
`missing`；只有得到安全终态后，`rin_consume_proposal(request_id)` 才返回
普通 JSON 兼容结果。状态为 `pending` 或 `unresolved` 时，应把
`rin_proposal_attempt(request_id)` 返回的普通记录随游戏存档持久化；重启后
把该记录交给 `rin_resume_proposal`，它会先恢复已知 Job，并且仅在 Rin 明确
确认该 Job 不存在时最多重发一次完全相同的请求。未决 Attempt 既不能消费，
也不能作为本地可确认取消处理。运行中的进程内 worker 则可由
`rin_cancel_proposal` 把取消传递给 Job API。

Python 客户端还提供 `commit_batch`、`set_actor_activity`、`arbitrate`、
`timeline`、`replay` 和结构化生成方法。Generation 必须与 Proposal 一样
使用进程内后台模式。`generate_json` 只接受不含供应商信息的 Rin 请求契约，
返回一个解码后的 JSON Object 和受长度限制的运维元数据。若游戏持久化请求
记录，应只允许所需字段；供应商模型名可用于显式探测，但不应写入玩法存档。

线程、取消事件、HTTP 对象和注册表都只属于当前进程。不要把它们赋给
`default`、persistent 数据、rollback 状态或存档对象。只保存已接受的协议
Snapshot、普通结果字典，以及上文所述的普通稳定 Proposal Attempt 记录。

即使开发者 shell 配置了端点，Ren'Py 原生测试也默认离线；只有
`RIN_LIVE_TEST_ENABLED=1` 才允许真实网络。

## Godot 4

将[客户端](../examples/godot/rin_client.gd)添加为节点或 autoload。
`propose_with_fallback` 等待 `HTTPRequest` signal 和 timer tick，不会阻塞
渲染。[NPC 示例](../examples/godot/example_npc.gd)展示提案、游戏应用和
结果回报顺序。示例中的存储方法是有意保留的集成 Hook，并不是内存持久化
实现；请用游戏存档系统替换 `_load_authoritative_state`、初始化、Attempt、
事务、转换和确认 Hook。在加载 Hook 返回一个有效状态，或明确确认
`not_found` 之前，示例会禁用 Turn，且不会向 Rin 发请求。

启用玩法前，必须把 run ID、稳定 Create 请求、操作序号、协议 tick 高水位、
完整 Proposal Attempt、applied marker 和 report Outbox 作为同一个游戏权威
状态恢复。tick 高水位可防止引擎帧计数在重启归零后产生
`tick_regressed`。I/O、解析或 schema 错误不等于 `not_found`，此时必须
fail closed，不能生成新身份；只有确实 `not_found` 时，才可先持久化完整
初始化状态，再发布新 run ID。

Godot 负责导航、动画、战斗、背包和对白渲染。Activity、到期角色、仲裁、
批量提交、时间线和回放 helper 都是 coroutine；只在模拟或区域变化时更新
Activity，不要每帧调用。适配器限制响应字节、禁用重定向，并只对精确的
loopback 主机和合法端口接受明文 HTTP。

## Unity

将 [RinClient.cs](../examples/unity/RinClient.cs) 挂载到 GameObject。它使用
`UnityWebRequest` coroutine 和有上限的流式下载处理器，不需要额外 JSON
或网络包。[RinNpcExample.cs](../examples/unity/RinNpcExample.cs)展示同样的
先应用、后回报流程及同样的启动恢复门禁。请把
`LoadAuthoritativeState` 和各持久化方法接入游戏存档；未配置时示例会有意
保持禁用，而不会把存储失败当作新周目。恢复的 Unity 状态必须包含上述同一
run ID、稳定 Create 请求、序号、tick 高水位、Proposal Attempt、applied
marker 和 Outcome Outbox。

Unity 的 `JsonUtility` 适配器为 Activity、调度、仲裁、批量提交和时间线
提供可序列化 DTO。由于 `JsonUtility` 无法表示以 Actor ID 为键的 map，
Replay helper 只返回已验证的 Snapshot header；需要完整回放状态的项目应
使用现有的字典型 JSON 包解析同一端点。使用动作参数 map 的游戏也可扩展
可序列化请求类，无需修改线上协议。

## 兼容性测试向量

`compat/` 下的可执行测试向量覆盖完整使用流程，但不会把某个使用方写成
Rin 公共定位的一部分。测试内容包括：

- 权限压力触发本地边界拒绝；
- 角色特定的观察和认知可见性；
- 目标驱动的可选内容；
- 接受提交、冷却调度和过早提案拒绝。

参考流程还组合了：

- 内容绑定与每次游戏运行一个 Rin Session；
- 将权威游戏事件转为 Actor 范围的 Observation；
- 将玩家自由文本转为显式 Observation；
- 在结构化内容生成前提供仅候选的角色方向；
- 接受方向并 Commit，再把 Snapshot 存入游戏存档；
- 同时根据已存 Snapshot 与当前 Sidecar head 派生 Restore ID；
- Sidecar Generation 不可用时使用确定性的 authored fallback。

运行公共兼容性测试：

```bash
go test ./compat
```

测试向量只包含 ID、契约、哈希和短测试事件，不包含使用方的完整内容或任何
供应商凭据。使用方专用的源码校验可以与测试向量放在一起，但不属于公共协议
契约。

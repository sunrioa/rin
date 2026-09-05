# 内部 Agent Runtime

[English](internal-agent-runtime.md) | [简体中文](internal-agent-runtime.zh-CN.md)

内部 Agent Runtime 是 `rin-control` 的可选组件。它使用人格、记忆、Skill 和模型
持续推进有预算的任务，但所有世界读取、控制租约、策略判断、Operation 和权威结果仍
经过同一个 Control Plane。未提供 Agent 配置时，`rin-control` 保持原有行为。

Runtime 当前推进显式创建的 Task，不会仅凭 Persona 的 `initiative_policy` 在后台
凭空创建新任务。游戏可以根据可信事件创建“主动问候、检查玩家状态、继续未完成话题”
等 Task；Initiative 字段用于约束模型在该 Task 内的表达与连续行动。这样主动性仍有
可见来源、冷却和取消入口。

## 身份边界

| 身份 | 来源 | 权限 |
| --- | --- | --- |
| Control Client | `RIN_CONTROL_PRINCIPAL` 与 `RIN_CONTROL_SCOPES` | `/control/v2` |
| Agent Client | 配置中的 `client_principal` | 仅 `task.read`、`task.execute`、`task.cancel` |
| Internal Runtime | 进程内创建，不通过 HTTP 暴露 | 仅控制 `DecisionAuthority=internal` 的角色 |

`RIN_CONTROL_TOKEN` 与 `RIN_AGENT_TOKEN` 必须使用不同值。任一 Token 都不能访问
另一组路由。Agent Client 不能配置 `host.admin`、`actor.*` 或游戏专属 Scope。

## 配置

创建只允许当前用户读取的 JSON 文件，例如 `/absolute/path/agent.json`：

```json
{
  "contract_version": "rin.agent.config/v1",
  "client_principal": {
    "id": "rin.agent-client",
    "granted_scopes": ["task.read", "task.execute", "task.cancel"]
  },
  "runtime_principal_id": "rin.internal",
  "model": {
    "provider": "openai-compatible",
    "base_url": "https://api.example.com/v1",
    "model": "example-model",
    "response_format": "json_schema",
    "authentication": "bearer-env",
    "max_context_characters": 64000,
    "max_output_tokens": 1500,
    "temperature": 0.2
  },
  "personas": [{
    "persona_id": "companion",
    "version": "v1",
    "identity": "A grounded companion.",
    "initiative_policy": {
      "enabled": true,
      "cooldown_millis": 5000,
      "max_consecutive_actions": 4
    }
  }],
  "persona_bindings": [{
    "persona_id": "companion",
    "version": "v1"
  }],
  "memory": {
    "semantic_embedding": {
      "enabled": true,
      "provider": "openai-compatible",
      "base_url": "https://api.example.com/v1",
      "model": "example-embedding-model",
      "authentication": "bearer-env",
      "allowed_domains": ["actor-episodic", "actor-semantic"],
      "min_local_matches": 4,
      "max_semantic_results": 4,
      "timeout_millis": 1500
    }
  },
  "learning": {
    "enabled": true,
    "publish_mode": "draft",
    "min_actions": 3,
    "adapter": "minecraft",
    "max_output_tokens": 1200
  }
}
```

不填写 `actor_id` 的绑定是运行时动态 Actor 的显式默认人格。精确的
Actor+Controller 绑定和 Actor 绑定优先；只能配置一个默认绑定，且默认绑定不能选择 Controller。

```bash
chmod 600 /absolute/path/agent.json
export RIN_AGENT_CONFIG=/absolute/path/agent.json
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="local.player"
export RIN_AGENT_TOKEN="$(openssl rand -hex 32)"
export RIN_AGENT_API_KEY="provider-key-from-secret-store"
export RIN_AGENT_EMBEDDING_API_KEY="embedding-key-from-secret-store"
./bin/rin-control
```

API Key 不是 Agent 配置字段；在 JSON 中加入 `api_key` 会被拒绝。除了以上环境变量，
回环地址上的 Rin Console 可将模型与 Embedding Key 保存到
`<data>/agent/agent-secrets.json`。该文件权限为 `0600`，接口只返回是否配置，环境变量会
覆盖本地值。连接无认证的本地
OpenAI-compatible 服务时，把 `authentication` 设为 `none` 并确保
`RIN_AGENT_API_KEY` 未设置。启动只校验配置，不向模型服务发送探测请求。

对于 DeepSeek 这类支持 JSON Object、但不支持 JSON Schema 响应格式的服务，将
`response_format` 设为 `json_object`。Rin 会把 Schema 放入稳定系统消息，并继续在
本地严格校验返回对象。`thinking_mode` 是可选字段；只有所选 OpenAI-compatible
服务实现了该请求字段时，才配置为 `enabled` 或 `disabled`。
DeepSeek V4 Flash 的低延迟动作决策配置为：`base_url: https://api.deepseek.com`、
`model: deepseek-v4-flash`、`response_format: json_object`、
`thinking_mode: disabled`。这只是 Adapter 配置示例；Persona、Memory、Skill、Agent Loop、
Control Plane 与 Host 契约均不依赖该供应商或模型名。

## Persona、Memory 与 Skill

`PersonaProfile` 只描述身份与表现：Identity、Traits、Values、Voice、Boundary、
Relationship Stance、Initiative 和 Presentation Rule。它不包含 Scope、Policy Rule、
API Key 或可执行 Hook。Persona Binding 按 Actor+Controller、Actor、默认值的顺序选择。

Memory 通过 Namespace 结构化隔离：

| Domain | 可见范围 | 用途 |
| --- | --- | --- |
| `actor-episodic` | 同一 Actor 的 Controller | 共同经历 |
| `actor-semantic` | 同一 Actor 的 Controller | 稳定偏好、承诺和关系事实 |
| `controller-working` | 当前 Controller | 当前任务工作记忆 |
| `controller-private` | 当前 Controller | 私有思考和外部不可见内容 |
| `controller-belief` | 当前 Controller | 尚未证实的判断 |

每条 Memory 有来源、是否权威、置信度、重要度、TTL、Subject、Tag 与 Supersedes。
模型生成的 Memory Candidate 永远是非权威主观记录；Host Outcome 才能形成权威世界
证据。检索受条数和字符预算约束，不会把全部历史塞入 Prompt。Forget 使用 Tombstone，
Consolidate 可以把多条记录压缩为带来源的摘要。

默认检索完全离线：SQL 先按 Session、Actor、Controller、Domain、Tombstone 和过期时间
裁剪合法记录，再合并近期候选、`unicode61` FTS5/BM25 与中日文子串使用的 trigram FTS5。
短于 3 个字符的查询不会触发 trigram 扫描，最终结果按来源排名和既有 Memory 分数稳定合并，
然后应用记录数与字符预算。当前没有 Wiki、关系图、Reranker 或后台向量服务。

远端语义检索只是可选补充。只有显式设置
`memory.semantic_embedding.enabled=true`、配置 OpenAI-compatible Endpoint/模型，并在
`allowed_domains` 中允许非私有 Memory Domain 后才会启用。只有复杂计划任务会在本地召回
不足时请求语义检索。文档向量通过有界后台队列生成，按配置模型与内容摘要写入可重建的
`memory_embeddings` 表；Query Vector 使用小型进程内缓存。向量候选进入结果前仍会重新校验
当前可见域、过期、过滤条件和内容摘要。

Embedding 凭据不进入 JSON，可以来自 `RIN_AGENT_EMBEDDING_API_KEY` 或 Console 管理的
secret 文件，环境变量优先。Controller 私有域和疑似凭据文本不会外发。超时、断网、限流、
模型身份不符、维度变化或非法向量都直接回退到 FTS5 与近期记忆，不中断游戏任务。Rin
不下载或托管 Embedding 模型；远端 Provider 也不拥有 Memory ID、事实、权限或删除权。

`rin-control` 独占 `<RIN_CONTROL_DATA_DIR>/agent/memory.db`。SQLite 是 Rin Memory 域
唯一的在线事实源，使用 WAL、完整同步和 FTS5；JSONL 仅用于显式的手动交换，不存在并行的
JSON 持久化后端。无论动作来自内部 Agent、外部 MCP 还是 Macro，只有
Control Plane 已提交的 Host Outcome 才会写入共享 `actor-episodic` 记忆，并保存
Host、World、Epoch、Sequence 与 Digest 的 `canon_ref`。它只是对游戏 Canon 的可检索
投影，不能反向修改 Canon。

Skill 是惰性的过程指导，只包含摘要、触发 Tag、说明和 Digest。它没有执行入口、
Scope 或 Capability Grant。模型先看到摘要，最多按需展开一个 Skill；即使 Skill
文字要求越权，模型输出仍受允许 Capability、Binding 和 Policy 约束。

Skill Catalog 由 `rin-control` 持有，而不是由 Internal Runtime 私有持有。配置内置
Skill、`skills/installed` 与 `skills/learned` 被合并成同一确定性目录；内部 Agent
直接使用该目录，外部 MCP 则通过 `skill.read` / `skill.write` 访问同一实例。未启用
内部模型时，外部 MCP 仍可使用 Skill。

外部 MCP Controller 的人格和私有记忆由外部 Agent 管理，不会被 Internal Persona
覆盖，也不会自动复制进 Rin Memory。

`learning` 默认关闭。开启后，只有至少完成 `min_actions` 次动作、且时间线包含 Host
权威成功 Outcome 的已完成任务才会额外调用一次模型生成经验 Skill。默认
`publish_mode=draft`，文件写入 `skills/drafts`，不会进入活动 Catalog；明确改成
`learned` 才写入 `skills/learned` 并在后续任务中可见。草稿输入不含 Operation ID、
HostRef、坐标、世界 UUID 或密钥，Skill 的 Adapter 和 Capability 由 Rin 根据证据绑定，
不能由总结模型扩大。学习失败不改变原任务结果。

## 模型决策

每次 Completion 请求先发送固定协议，再发送一条确定性序列化的静态上下文消息，其中包含
决策 Schema 摘要、Persona、按 ID 排序的 Capability 摘要和 Skill 摘要。Task ID、目标、
Observation、Epoch、Target、检索记忆、展开内容和 PlanState 都留在最后一条动态消息。该字节
稳定前缀允许兼容供应商直接使用自身 Prompt Cache，不需要 Rin 再建设缓存服务。Persona、
Capability Spec Digest、Skill Digest 或决策 Schema 改变时，私有 DecisionRecord 中的
`stable_prefix_digest` 也会改变。

OpenAI-compatible Adapter 会把供应商实际返回的缓存命中、未命中和写入 Token（包括常见兼容
别名）映射到 `provider.Usage`；任务时间线只记录这些实测值。供应商未返回的字段保持“未知”，
不会伪装为零。Rin 不缓存 ActionRequest、Observation、PolicyDecision 或游戏 Outcome，也不
默认发送供应商专属缓存参数。

若 Provider 需要把通用响应 Schema 转成提示词，它通过可选的请求预处理接口返回最终消息；
Rin 随后才检查上下文并计算请求摘要和稳定前缀。无需转换的 Provider 不实现该接口即可，
Resilient 只透明转发，不在核心引入供应商分支。

一次模型输出只能是 `action`、`wait`、`complete` 或 `inspect`：

- `action` 选择一个允许 Capability、严格 JSON 参数和已列出的 Target Handle；
- `inspect` 最多展开 4 个 Capability 和 1 个 Skill，并且最多一轮；
- `wait` 表示当前没有有根据的行动；
- `complete` 仅提出完成请求，必须满足调用方选定的验收策略，不能覆盖该策略。

可信 Contract 与 `untrusted_context` 分离。Persona、Memory、Skill、Observation、
玩家文本和 Capability Description 都属于不可信数据，不能改变允许集合、Epoch、
Controller 或预算。

## 状态与调用

- Task HTTP 契约以 [`api/agent-openapi.json`](../api/agent-openapi.json) 为准。
- 状态固定写入 `<RIN_CONTROL_DATA_DIR>/agent/tasks.db` 和 `memory.db`。
- Task 使用 SQLite Schema 2，任务投影为 `rin.cognition.tasks/v5`。首次创建数据库时
  导入已有 `tasks.json` 的 v3、v4 或 v5 快照；后续打开只读取数据库。
- Task CAS 在同一事务内更新一个任务行和快照 Revision。WAL、`synchronous=FULL`、
  私有文件及单写者进程锁保留成功返回前已持久化的保证；提交失败后缓存不可读，需重新打开。
  配置不能改变状态路径。详见[存储迁移](execution-storage.zh-CN.md)。
- `scheduled=true` 只表示任务已进入后台队列，不表示模型已决策、游戏已执行或
  目标已经完成。
- `allowed_capabilities` 是可选的任务级 Capability ID 白名单，最多 128 项。非空时，
  Runtime 只向模型公开 Host 当前 Catalog 与该白名单的交集，并在恢复 Pending Action 时
  再次复验；空数组表示使用 Host 当前完整 Catalog。该字段只能收窄能力，不能创建 Host
  未发布的能力或绕过 Policy。
- 守护进程先停止 Agent worker 并关闭 Task 状态，再停止 Control Plane 投递 worker，
  最后关闭共享 Plan 与 Memory 存储。

## 调度与取消

`TaskSession.schedule` 是持久化调度状态。History 仅用于诊断，新增警告或 Signal
事件不会改变任务是否应当执行。

| `schedule.kind` | 继续条件 |
| --- | --- |
| `ready` | 入队执行一次有步数上限的推进。 |
| `waiting-observation` | Actor 在线，且发布了更新的 Observation Sequence 或不同 Epoch。 |
| `waiting-operation` | Operation Cursor 改变、操作结束或需要对账。 |
| `waiting-confirmation` | 确认或取消使被跟踪的 Operation 发生变化。 |
| `retry-at` | 到达已保存的重试时间。 |
| `waiting-user` | 根据任务状态，由用户显式请求运行或恢复。 |
| `stopped` | 停止继续决策；未知结果仍跟踪原始 Operation 进行对账。 |

模型返回 `wait` 时保存当前 Observation 的 Epoch 和 Sequence；等待 Operation 或确认时
保存 Operation ID 和 Cursor，然后释放 worker。Task 与 Control Plane 通知按世界、Actor
或 Operation 索引选择受影响的活动任务，不复制历史上下文。恢复扫描负责通知遗漏及进程重启，
默认每 5 秒执行一次；暂时性故障在 5 秒后具备重试资格。
带 Plan 的任务还会等待 `task-plan` 订阅者确认结果投影；Memory 回写不影响任务就绪条件。
`OperationWaitMillis` 保留以兼容已有调用代码，但不再控制 worker 等待。

导入 v3 快照时只推断一次旧等待条件；缺少 Observation 记录的旧 `wait` 需要显式运行。
v4、v5 快照必须包含有效的 schedule；旧任务默认使用模型声明完成。原 JSON 仅作为
迁移备份保留，不是供旧版程序继续使用的实时副本。

取消先持久化 `cancel_requested`，再取消当前运行的 Context，不等待任务执行锁。
迟到的模型输出会被丢弃。`action_submission_started` 记录 Pending Intent 可能已经到达
Gateway 的阶段。恢复时通过进程内 `FindActionOperation` 查询原始提交，取消路径不会
重新提交 Action。如果可能已提交却查不到 Operation，任务进入 `outcome-unknown`，代码为
`action.submission-unknown`；不能仅凭查无记录宣称动作没有发生。

取消请求不能撤销 Host 已产生的 Effect；已提交的操作在权威结果确定前保持 `cancelling`。
自定义模型 Provider 与 Outcome 订阅者需要响应 Context 取消，及时释放自身资源。

## 独立目标验收

`StartTaskInput.completion` 独立于 `planning_mode`，不必为简单目标创建 Plan：

| `completion.mode` | 完成条件 |
| --- | --- |
| `model-declared`（显式选择） | 模型请求完成，且已有 Plan 已完成。 |
| `host-evidence` | 模型请求完成，且调用方提供的全部条件有 Host 证据。 |
| `human-confirmation`（新任务默认） | 模型请求验收后，由具备 `task.execute` 权限的调用方确认精确任务版本。 |

`host-evidence` 接受 1–16 个采用 Plan Condition 结构的条件。`observation-fact` 精确
匹配标量 `fact_value_json`，全部事实条件必须同时成立于当前观察；`operation-outcome`
指定精确的 Capability ID 与版本，要求本任务在当前 Epoch 内得到已确认的成功结果。
不相关动作、旧 Epoch 结果以及不同时刻拼凑的事实均不能完成验收，模型也不能改写调用方
提供的条件。证据不足时等待新观察；已提出完成请求后，新证据可直接完成任务，无需额外模型调用。

```json
{"mode":"host-evidence","conditions":[{"condition_id":"goal.arrived","kind":"observation-fact","summary":"Host 确认到达。","fact_id":"actor.at-destination","fact_value_json":"true"}]}
```

已有任务保留所记录的验收策略，未记录策略的旧快照继续采用模型声明完成。短期自动主动
任务显式选择模型声明；调用方目标默认人工确认。

`completion.operation_requirements` 可按 `condition_id` 收紧动作条件：
`arguments_json` 精确匹配整个参数对象（忽略键顺序，数字按 JSON 表示精确匹配）；
`target_refs` 匹配 Host 绑定后的真实目标引用及 Epoch；`minimum_count` 要求 1–64 个
不同的成功 Operation，同一 ID 的重试不能重复计数。条件在任务内不可改写；这里统计的是
操作次数，不是物品总数。持续时长、库存总量等复合目标应由 Host 完整判断后发布标量事实，
例如 `goal.bridge-held-30s=true` 或 `goal.has-ten-wood=true`。

```json
{"mode":"host-evidence","conditions":[{"condition_id":"goal.collect","kind":"operation-outcome","summary":"成功采集木材两次。","capability":{"id":"game.item.collect","version":"1.0.0"}}],"operation_requirements":[{"condition_id":"goal.collect","arguments_json":"{\"item\":\"wood\"}","minimum_count":2}]}
```

人工验收时暂停码为 `completion.confirmation-required`。向
`POST /agent/v1/tasks/confirm-completion` 提交 `task_id` 和 `expected_revision`；
过期版本返回冲突。确认不能覆盖取消、未结束的动作或未完成的 Plan；恢复任务则重新进入
决策。Console 创建表单提供验收模式，任务进入人工验收后显示确认按钮。

运行流程已拆成上下文收集、有界模型决策和应用决策。显式 `inspect` 与参数 Schema 修复
共享至多一次追加模型调用的预算，同时保留各自的诊断事件。

## Macro 父子循环

内部 Runtime 与外部 MCP 使用同一套父子 Operation 契约。模型选择声明
`kind=macro` 且 `produces_child_operations=true` 的能力后，只有 Host 将父 Operation
推进到 `accepted` 或 `running`，Task 才记录 `macro_operation_id` 并进入下一次观察。
后续模型请求携带可信 `parent_operation_id`，所选 Atomic Child 仍逐项经过 Host Binding、
Policy、执行与权威 Outcome。

- queued、delivered、awaiting-confirmation、accepted 和 running 都不是完成证据；父 Operation 只有
  权威终态后才从 Task 清除。
- 运行父 Macro 时，模型只看到 Atomic Capability；Control Plane 仍支持嵌套 Macro，但当前
  内部 Runtime 不自动创建第二层父任务。使用任务级白名单时，必须同时列出父 Macro 和
  预期 Child；进入 Macro 阶段不会自动扩大任务权限。
- 有运行中 Child 时取消 Task，会先取消 Child，再取消 Parent；父操作稳定终止前 Task 保持
  `cancelling`。
- Child 或 Parent 的 `outcome-unknown` 会保留准确 Operation ID，并停止继续决策。
- ActionGateway 在入队前拒绝时，只在任务历史记录 `gateway.stale`、
  `gateway.lease-expired`、`gateway.forbidden` 或 `gateway.invalid` 等稳定类别；
  Provider 文本和内部错误详情不会进入任务历史。
- Provider 故障或预算耗尽会暂停而不是释放控制后遗留父 Macro；用户仍可恢复或取消 Task。

模型只能提出基于当前 Observation 和 Capability 的 ActionRequest。Host 仍负责绑定
目标、预览 Effect、执行 Policy、修改世界并返回 Outcome；人格、记忆或 Task Token
都不能授予世界权限。

## 异常恢复与历史保留

未知结果停止模型决策，但持续核对原始 Intent 或 Operation，以及必要的 Plan 投影。
显式运行也可触发只读结果核对，不会重发动作。Host 补齐终态后，任务继续或完成取消，
不会因历史 unknown 永久阻止该角色的 Signal 协调。

Plan 使用确定的 `plan.<TaskID>` 身份。若 Plan 已提交而 Task 引用尚未保存，重启会检查
Task、Host、World、Actor、Controller、Session、Goal 和规划归属后接续已有计划。
保存的 Plan Epoch 继续约束证据及动作授权；Epoch 改变后须复验或重规划。取消也会查找
尚未写入 Task 引用的自有 Plan。Task/Plan 归档、Signal 持久化与增量决策日志详见
[存储与迁移](execution-storage.zh-CN.md)。

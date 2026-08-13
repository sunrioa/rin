# 任务计划

Rin Task Plan v1 是可选的、引擎中立的复杂任务进度记录，用于需要多个权威游戏动作的工作。
它不是第二套策略引擎，也不会自行执行动作。

简单任务使用 `planning_mode=disabled`。`auto` 模式允许 Agent 在第一次结构化决策中同时附带
粗粒度计划；`required` 模式在没有计划时拒绝该决策。计划与动作来自同一次模型响应，因此
Rin 不会在每一步前额外调用一次 Planner。

与计划关联的动作携带 `plan_step_ref`。Task Coordinator 校验精确 Revision 和当前 Step 后，
仍把普通强类型 ActionRequest 交给 Host Binding、Policy 和 Operation。只有 Host 权威 Outcome、
当前 Observation Fact、玩家确认或 Host Condition 能满足已声明条件；模型文字不能推进步骤。

内部与外部 Agent 共用 `taskstate.db`。内部 Runtime 只在 TaskSession 保存 `plan_id`、Revision
和当前 Step。MCP 提供创建、读取、等待、修订、暂停、恢复、取消、请求迁移和提交步骤动作等
工具；外部路径不会调用内部模型，也不会采用内部 Persona。

计划最多 16 个 Step，使用 CAS Revision，每个角色同时只有一个活动计划，每个计划同时只有
一个未完成 Operation。重启后可恢复进度，但不会恢复旧授权；执行前仍重新校验 Controller
Lease、Epoch、Observation、Capability 和 Policy。

HTTP 契约见 `api/task-plan-openapi.json`，跨语言请求 Fixture 见
`api/task-plan-v1-fixtures.json`。

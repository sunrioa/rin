# 任务计划

Rin Task Plan v1 是可选的、引擎中立的复杂任务进度记录，用于需要多个权威游戏动作的工作。
它不是第二套策略引擎，也不会自行执行动作。

简单任务使用 `planning_mode=disabled`。`auto` 模式允许 Agent 在第一次结构化决策中同时附带
粗粒度计划；`required` 模式在没有计划时拒绝该决策。计划与动作来自同一次模型响应，因此
Rin 不会在每一步前额外调用一次 Planner。

与计划关联的动作携带 `plan_step_ref`。Task Coordinator 校验精确 Revision 和当前 Step 后，
仍把普通强类型 ActionRequest 交给 Host Binding、Policy 和 Operation。只有 Host 权威 Outcome
或当前 Observation Fact 能满足已声明条件；玩家或模型文字没有这些 Host 记录时不能推进步骤。
每个 `operation-outcome` 条件必须绑定一个精确 Capability，每个 `observation-fact` 条件必须
绑定一个精确 Host Fact ID 与期望标量值。无关动作即使成功、Fact 值不符也不会推进步骤。Plan 不保存没有执行语义的
`preconditions`；准备要求写在步骤目标和 Skill 中，机器进度只使用可验证的成功条件。

内部与外部 Agent 共用 `taskstate.db`。内部 Runtime 只在 TaskSession 保存 `plan_id`、Revision
和当前 Step。MCP 提供创建、读取、等待、修订、暂停、恢复、取消、请求迁移和提交步骤动作等
工具；外部路径不会调用内部模型，也不会采用内部 Persona。

计划最多 16 个 Step，使用 CAS Revision，每个角色同时只有一个活动计划，每个计划同时只有
一个未完成 Operation。重启后可恢复进度，但不会恢复旧授权；执行前仍重新校验 Controller
Lease、Epoch、Observation、Capability 和 Policy。

同一失败族连续出现三次且都来自权威 Outcome 时，即使当前 Step 的 `max_attempts` 更高，
Agent 也可以修订方法；`max_attempts` 仍负责最终阻塞该步骤。执行前若控制权已被外部 MCP
或其他 Principal 接管，内部任务会丢弃尚未提交的动作并以 `controller.contended` 暂停，
不会跳过步骤、取消计划或每五秒抢回控制权。释放外部控制后再显式恢复任务，Runtime 会先
重新观察世界再生成下一动作。

Rin Console 的任务列表同时读取内部 TaskSession 和共享 PlanStore。仅由外部 MCP 创建的计划
会显示当前阶段、条件、证据、Revision 和控制来源，但不会出现内部任务的继续、恢复或取消
按钮；外部 Agent 仍通过 MCP 管理该计划。

HTTP 契约见 `api/task-plan-openapi.json`，跨语言请求 Fixture 见
`api/task-plan-v1-fixtures.json`。

## 已关闭计划的保留

容量只统计 planned、active、blocked、paused；关闭计划保留身份、关联和证据，不占活动
容量。列表返回活动计划及有界的近期已关闭历史，精确 Plan/Task 查询可读取更早记录。
内部任务若在 Plan 创建后、Task 引用提交前中断，会接续同一归属的 `plan.<TaskID>`，
不会再次创建同名计划而陷入冲突。

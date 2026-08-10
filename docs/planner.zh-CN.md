# 有界任务计划 DSL

`planner` 是 Rin 的 Go Host 库中与具体游戏无关的确定性计划状态机。它不读取世界、
不解析模型坐标、不调用模型，也不执行能力；Host 负责把权威事实转换为完整 Offer，并在
游戏线程验证效果。Rin 中央服务不会代替游戏适配器执行计划。

## 计划形状

计划由 `Plan`、`Node` 和 `Budget` 组成。节点可以是：

- `action`：绑定一个已注册能力；
- `branch`：由 Host 根据事实选择 `then` 或 `else`；
- `loop`：拥有明确的 `max_iterations` 和子节点。

`Validate` 会检查标识符、`low|moderate|high|critical` 风险枚举、节点数量、最大深度、
依赖/分支/循环引用、控制节点唯一归属、优先级、尝试次数和预算。Action 只能绑定能力；
Branch 的 `then`、`else` 路径不能重叠；Loop 必须拥有子节点和显式上限。图引用按执行方向
做环检测，有界循环不能用隐式图环替代。

Wire 契约与单个计划的修订号分别版本化。每份计划都必须携带 `"schema_version": 1`；
不支持的版本会在图校验前被拒绝。规范位于 `planner/schema/plan-v1.schema.json`，Fixture
位于 `planner/testdata/plan-v1.json`，两者会同时对照 Go 运行时测试。`revision` 表示同一
业务计划的内容修订，`schema_version` 表示 DSL 的结构与执行语义版本。

## 确定性状态转换

Host 按以下顺序使用：

```go
state, err = plan.Advance(state, facts, absoluteTick)
actions := plan.Ready(state, nil)
// 执行一条完整、已绑定的 Host Action，并验证后置条件。
state, err = plan.Apply(state, actions[0].ID, absoluteTick, verifiedMutations)
// 或记录一次已确认失败，节点仍可在尝试预算内重试。
state, err = plan.Fail(state, actions[0].ID, absoluteTick, verifiedMutations)
```

`Advance` 会记录唯一分支、跳过另一条路径、激活循环子树、在迭代之间重置子树，并在事实
条件不满足或达到 `max_iterations` 时结束循环。`Ready` 只返回 Action，并按优先级从高到低、
再按节点 ID 排序。

## 执行门禁

Host 在应用一个已验证结果前调用 `Plan.Allows`，检查：

- 计划步骤、世界修改和从首次转换开始计算的绝对 tick 预算；
- 节点最大尝试次数与依赖完成情况；
- 节点是否属于当前已选择的分支或活动循环；
- 本次实际世界修改数不超过节点声明值。

`Apply` 和 `Fail` 都会消耗尝试次数及全局步骤预算，只返回新的 `State`，不会修改调用方的
map。节点耗尽重试后会进入明确的 `Failed` 状态，计划停止调度其他动作。`Done` 表示计划已
进入成功或失败终态，`Succeeded` 仅在所有必要子树成功完成时返回 true。

`ValidateState` 会拒绝伪造、越界或互相矛盾的状态，包括没有分支依据的 `Skipped` 节点、
没有执行尝试却被标记完成的 Action、无效失败状态以及倒退的绝对 Tick。恢复持久化状态后，
Host 必须先验证状态，再继续转换。

`State.steps` 是已验证 Action 尝试的累计数。循环进入下一轮时会清除上一轮节点上的
`attempts`，状态机同时把这些已清除尝试计入 `retired_steps`。持久化状态必须始终满足
`steps = sum(attempts) + retired_steps`；`ValidateState` 还会根据已记录循环次数和受控子树的
最大尝试数限制 `retired_steps` 上限。该字段是由 Host 维护的执行历史，不是 Agent 可修改的
预算旁路。

游戏适配器应同时保存 State、计划修订、Offer digest、Epoch 和 Outcome。结果不确定时
必须进入暂停或 `outcome_unknown`，不能调用 `Apply`/`Fail`，也不能自动重放。

非 Go 适配器可以实现同一转换契约；Minecraft 的方块、配方、寻路和权限规则仍留在 Mod
中，不会被中央 Planner 或外部 Agent 绕过。

当前 Minecraft 适配器尚未消费这份可执行 Plan v1 文档。它返回的
`active_plan.task_graph` 是 Java Host 控制器的版本化只读状态投影，供外部 Agent 观察
权威进度；该投影不能作为 Planner 计划回传，也不代表 Agent 获得任意节点执行权限。

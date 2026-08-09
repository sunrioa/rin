# 有界任务计划 DSL

`planner` 是 Rin 中与具体游戏无关的计划形状和预算门禁。它不读取世界、不解析模型
坐标，也不执行能力；Host 负责把事实转换为完整 Offer，并在游戏线程验证效果。

## 计划形状

计划由 `Plan`、`Node` 和 `Budget` 组成。节点可以是：

- `action`：绑定一个已注册能力；
- `branch`：由 Host 根据事实选择 `then` 或 `else`；
- `loop`：拥有明确的 `max_iterations` 和子节点。

`Validate` 会检查标识符、节点数量、最大深度、依赖/分支/循环引用、优先级、尝试次数和预算。
图引用按执行方向做环检测；有界循环不能用隐式图环替代。

## 执行门禁

Host 在应用一个已验证结果前调用 `Plan.Allows`，检查：

- 计划步骤、世界修改和 tick 预算；
- 节点最大尝试次数与依赖完成情况；
- 本次实际世界修改数不超过节点声明值。

`Ready` 按优先级从高到低、再按节点 ID 排序，保证相同事实下的选择稳定。

`Plan.Apply` 只返回新的 `State`，不会修改调用方的 map。游戏适配器随后保存游标、
计划修订、Offer digest、Epoch 和 Outcome；结果不确定时必须进入暂停或
`outcome_unknown`，不能自动重放。

Minecraft Mod 的 `CompanionDynamicTaskController` 是该边界的适配器实现，具体方块、
配方、寻路和权限规则仍留在 Mod 中。

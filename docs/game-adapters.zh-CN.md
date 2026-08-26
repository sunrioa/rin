# 游戏 Adapter 指南

[English](game-adapters.md) | [简体中文](game-adapters.zh-CN.md)

具体游戏的集成边界集中在 Adapter。Rin 不要求游戏使用 ECS、行为树、特定脚本语言
或特定网络架构；Adapter 的完整职责以下方“必须实现”接口为准。

## 必须实现

Go HostKit 用以下接口表达完整边界，其他语言应保持相同语义：

```text
Manifest()
Snapshot(target)
Observe(query)
ListCapabilities()
Bind(target, request)
Preview(target, request, binding)
Execute(operation)
Cancel(operation)
Verify(operation)
PolicyFacts(target)
```

`Snapshot`、`Observe`、`Bind`、`Preview`、`Execute`、`Cancel` 和 `Verify` 都可能
接触游戏状态，必须通过游戏自己的 Server/Main/Authority Thread Dispatcher。

## 身份映射

Adapter 必须从游戏存档生成稳定身份：

- `host_id`：一种 Adapter 或服务实例，不应包含临时端口；
- `world_id`：当前存档/世界的稳定 ID；
- `actor_id`：同一存档内角色的稳定 ID；
- `Epoch.host`：Host 进程或权威实例变化时递增；
- `Epoch.world`：场景、维度或地图重载时递增；
- `Epoch.timeline`：读档、回滚或分支时递增；
- `observation_sequence`：同一 Epoch 内单调递增。

不要用实体内存地址、临时 Runtime ID、显示名或当前位置作为长期身份。

## Observation 设计

模型需要足够自由地判断，但不需要完整世界转储。优先发布：

- Actor 自身状态、当前位置、当前活动和危险；
- 经过范围和数量限制的附近实体、资源和交互对象；
- 物品、目标和区域的所有权与 Scope；
- 当前长任务、失败原因和可取消状态；
- 与当前任务相关的事实，而不是所有历史。

大集合使用分页。对象通过不透明 `HostRef` 暴露，文件路径、对象指针、NBT/组件
私密内容、Token 和服务器秘密不得进入 Observation。

## Capability 设计

Capability 应表达游戏中稳定、可验证的语义动作，例如：

```text
navigation.move_to
resource.harvest
inventory.transfer
crafting.craft
combat.attack
dialogue.say
quest.accept
building.place_batch
```

强模型 Harness 的重点是“告诉模型不能造成什么”，而不是用大量固定任务教它每一步。
因此建议：

- 提供少量组合性强的原子能力；
- 参数与目标由 Schema 和 HostRef 约束；
- 用 Effect 表达资产、区域、风险和数量；
- 允许模型依据 Outcome 自行重新规划；
- 仅为高频、可稳定实现且能节省大量决策的工作提供 Macro。

不要为每种材料、敌人或剧情分支创建一个能力。也不要提供 `execute_anything`、任意
类名、任意脚本或可直接访问引擎对象的万能能力。

## 原子能力与实时控制器

一个语义动作可以在游戏中持续很多 Tick。模型提交一次 `navigation.move_to` 后，
Adapter 的导航控制器逐 Tick 寻路、避障和检测危险；模型不需要连续发送前进键。

长任务应：

- 可查询进度并使用单调 `progress_seq`；
- 在目标消失、Epoch 变化、超预算或环境危险时停止；
- 支持声明的 cooperative/preemptive 取消；
- 在完成后返回实际数量、位置、目标和失败码；
- 不把“已启动”包装成“已完成”。

## Binding 与 TOCTOU

`Bind` 解析 Controller 意图但不执行世界修改。`Preview` 根据已解析对象产生真实
Effect。进入 `Execute` 前仍要检查：

- Capability 仍发布且 Digest 一致；
- Controller、Actor、Epoch 和 Observation 仍匹配；
- 目标仍存在、仍在允许区域并且距离合理；
- 所有权、容器权限、工具、资源、冷却和游戏模式仍允许；
- Policy 允许的是同一个 Effect Digest；
- Operation ID 尚未应用或可以安全精确重试。

检查失败返回结构化错误，让 Controller 根据新 Observation 重新规划；不要静默换
目标或扩大范围。

## Effect 与本地规则

Adapter 必须把游戏规则映射为标准字段。例如采集方块可能生成：

```text
kind=world.resource
operation=delete
ownership=unowned
scope=world:wilderness
quantity=1
tags=[resource.common, tool.pickaxe]
risk=low
reversible=false
```

管理员可以配置已知 Kind/Scope、Rule 和 Budget。未知第三方内容应默认标成未知并
拒绝，或通过管理员白名单/游戏 Tag 显式分类；不能仅凭名称后缀猜测安全。

## 多人与最高权限

公开服务器可以支持自主 NPC，但默认关闭。Adapter 应提供游戏内配置来决定：

- 哪些玩家可以绑定或控制哪个 Actor；
- 哪些维度、区域、容器和资产可用；
- 自动执行、要求确认或永久禁止的 Effect；
- 动作次数、方块/物品数量、半径和持续时间预算；
- 命令、命令方块或管理员能力的精确白名单。

所谓最终档位仍只能开放已注册、可绑定、可审计的 Capability。控制台命令若确有
产品需求，应成为独立 critical Capability，参数使用枚举或严格 Schema，Effect
明确标记 `system` 所有权并要求 Owner/Admin 确认；不能开放任意命令字符串或权限
绕过。

## 内部与外部控制

Adapter 发布 `decision_authority`：

- `source=internal`：由 Rin Internal Agent 使用游戏配置的人格与记忆；
- `source=external`：由匹配 Principal 的外部 Agent 通过 MCP 控制；
- `persona_mode=character-bound`：外部 Agent 应保持游戏角色设定；
- `persona_mode=agent-avatar`：角色表现为外部 Agent 自身人格。

同一时刻只有一个 Controller Lease。外部 MCP 不应依赖内部思考器；Macro 和实时
执行器属于 Adapter 的确定性游戏能力，而不是内部模型。切换 Authority 时应递增
Revision、终止旧租约并隔离未接受的旧动作。

游戏 Mod 不需要实现自己的 MCP Server。它只实现 Host Control Client 和 Adapter，
所有兼容游戏复用同一个 `rin-mcp`。

## 持久化

游戏存档至少保存：

- 稳定 World/Actor ID 和 Epoch 高水位；
- `decision_authority`、权限配置和 Emergency Stop；
- 已接受但未完成 Operation 的必要恢复状态；
- 已应用 Operation Marker 与待发送 Outcome Outbox；
- 游戏拥有的长期任务和世界 Canon。

不要保存线程、Socket、Future、回调、引擎对象或 API Key。Host 重连后重新注册、
发布 Read Model，并用同一 Operation ID 对账。

## 验证

先用 Grid/Story Adapter 跑通契约，再在真实游戏覆盖：

- 主线程和服务器权威；
- 正常存读档、世界切换、断线和强制终止；
- 重投不重复效果；
- 急停和取消延迟；
- 多人权限和玩家资产；
- 长任务的真实 Outcome；
- UI 不阻塞渲染或 Tick。

完整清单见[集成验收](host-integration-validation.zh-CN.md)。

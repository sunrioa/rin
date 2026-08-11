# 路线图

[简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

Rin `0.7.0` 是 Preview、pre-1.0 软件。本文件是未来工作的唯一权威计划；
已经发布的变化见[变更日志](CHANGELOG.zh-CN.md)，协议事实见 OpenAPI 和专题文档。
临时设计稿在实现合并后删除，不继续作为第二份路线图维护。

当前状态：控制面和 Minecraft/Fabric 首个持续角色切片已完成自动化实现，进入
[真人验收](https://github.com/sunrioa/rin-mi/blob/main/docs/ACCEPTANCE.zh-CN.md)。
未通过真人门槛前，不扩展通用任务图、大型蓝图或多伙伴。

## 产品方向

Rin 的首要目标是帮助游戏实现同一存档中持续存在的角色：

- 角色记得自己合理知道的事实、关系和未完成目标；
- 模型可以提出对白、意图和计划，但不能直接修改权威世界；
- 游戏内 AI、命令、Mod API 和 MCP 最终进入同一条 Host 执行链；
- 在线模型、主动联系和外部控制都可以关闭；
- Minecraft/Fabric 是当前最深的参考场景，协议仍保持引擎无关。

Rin 不是游戏引擎、通用自动化平台或模型代理服务。渲染、导航、物理、战斗、
背包规则、任务规则和最终权限始终属于游戏 Host。

## 当前已支持

### Runtime 与持久状态

- 多角色 Session、Observation、Memory、Belief、Goal 与边界。
- 确定性 Policy，以及可选的 OpenAI-compatible 在线 Provider。
- Action Proposal、Attempt、Run、Outcome 和崩溃恢复。
- Hash-chained 事件日志、Snapshot、Replay、Timeline 和 Session Transfer。
- 有界异步 Generation、Speech、Memory Summary 与 Telemetry 端口。

### Host 契约

- 引擎无关 Manifest、Epoch、Capability、Offer、Invocation 和 Outcome。
- JSON Schema 参数校验、Descriptor Digest、动态撤销和执行前 TOCTOU 复验。
- HostKit Coordinator、Pending Journal、Outcome Outbox 和长期动作恢复。
- Python、JavaScript、C#、Java、Lua 客户端及多种引擎参考适配器。

### 外部控制

- 常驻 `rin-control` 独占 7375 端口、状态目录和固定可信 Principal。
- 任意数量的 `rin-mcp` STDIO 薄代理可连接同一 daemon；Client 退出不会关闭 Host
  服务。
- Host 注册、租约、Read Model、消息、可拒绝 Directive、精确 Offer、取消、
  ACK、进度和 Outcome。
- MCP 和 Host API 共用稳定 Operation ID、Epoch/Observation Binding 与精确
  Host Offer。
- Host 发布单一 `decision_authority`，在内部控制与一个精确外部 Principal 之间
  转交；修订号会隔离旧回合。
- 外部控制器可以长轮询脱敏 Actor 状态，并以同一 `turn_id` 提交角色对白和精确
  Offer 选择。
- 状态目录独占锁、旧时间线失效、孤儿操作过期和有界恢复。
- 独立 [`api/control-openapi.json`](api/control-openapi.json) 契约及官方 MCP
  Conformance 的能力匹配门禁。

## 当前实施阶段

以下工作按顺序完成。前一项没有自动化证据时，不扩展后一项。

### 1. 控制面正确性与拓扑

- [x] MCP 与 HostKit 对精确 Offer 使用相同的预算和最终授权语义。
- [x] Directive 绑定提交时的 Epoch 和 Observation Sequence。
- [x] Control 状态目录跨平台独占锁。
- [x] 无 Host 的未完成 Operation 能过期，不永久占满队列。
- [x] 常驻 `rin-control` 与多客户端 `rin-mcp` 薄代理。
- [x] Control OpenAPI、路由漂移测试和官方 MCP Conformance 门禁。
- [x] 高频投递/进度只做可恢复检查点，耐久边界保留在入队、ACK、取消和 Outcome。

### 2. 文档与复杂度收敛

- [x] 删除已完成的一次性设计稿和重复 MCP 计划。
- [x] 以本文件统一当前阶段、未来阶段和明确非目标。

持续约束：

- 每次功能变更同时更新唯一的契约来源和一个用户入口，避免平行规格。
- 发布前根据真实玩家证据删除没有使用方、没有兼容承诺的 Preview 表面。

### 3. 单存档持续角色参考切片

该阶段主要在真实 Fabric Host 中落地，并用跨仓库契约测试证明通用性。

- [x] 将角色身份、Canon、记忆来源和未完成目标绑定到一个世界存档。
- [x] 区分玩家知道、角色知道、共同经历和角色尚未说出的事实。
- [x] 游戏内命令、内部 AI 与 MCP 共用同一个角色回合执行服务。
- [x] 让模型生成的对白经过校验后成为可追溯 Canon Event。
- [x] 对玩家原话做有界记忆召回，并避免其他玩家或旧时间线信息泄漏。
- [x] 提供在线与明确离线路径；离线角色允许明显更简单，在线故障不伪装成功。
- [x] 自动验证存档迁移、Pending Turn、实体卸载、重载和 Host 暂时离线恢复。
- [ ] 由真人确认死亡、维度切换、关系感和 30 至 60 分钟记忆体验。

### 4. 主动性与单一控制权

- [x] 主动对话默认关闭，并可配置主动级别、冷却和连续主动回合上限。
- [x] Dormant Actor 仅在主人在线、存在可继续话题且满足冷却时唤醒，不做无限后台轮询。
- [x] 角色可以发起有上下文对白、提出一个可审核小目标，并拒绝不安全动作。
- [x] 同一 Actor 只有一个决策控制源：内部 Runtime，或绑定一个精确 Principal 的
  外部 Agent；控制权修订会使尚未接受的旧回合失效。
- [x] 外部 Agent 可以等待状态变化、作为角色说话并选择 Host Offer；对白和动作可用
  同一个 `turn_id` 关联。
- [x] `character-bound` 与 `agent-avatar` 明确区分角色人格和外部 Agent 人格；
  Rin 不在控制源之间复制私有记忆。
- [x] 自动行为与外部入口共用 Capability、精确 Offer 和 Host 最终授权；语义决策
  与逐 Tick 执行保持分层。
- [x] 内部 Agent 可持久驱动一层 Macro 父 Operation 及其 Atomic Child；重启、确认、
  `outcome-unknown` 和先子后父取消均保留同一 ActionGateway 审计链。
- [ ] 安静时段、每日上限和更丰富的自主目标等待真人确认当前主动性有价值后再做。

### 5. 有界世界任务

- [x] 开放有限背包槽、普通容器摘要、低风险工具和附近白名单资源。
- [x] 每个世界动作由 Host 发布精确 Offer，模型不能构造任意物品 ID、坐标或方法名。
- [x] 完成固定 15 步采集、合成、建造闭环，支持确认、暂停、恢复、取消和重启恢复。
- [x] 对区块加载、方块/容器变化、资源数量、工具状态和能力撤销做 TOCTOU 复验。
- [ ] 只有第一个任务通过真人试玩后，才扩展更多物品、容器和任务类型。

## 真人验收门槛

自动测试完成后，以下项目必须由真人在真实游戏中确认：

- 角色在 30 至 60 分钟内记忆自然，不重复追问，也不引用不应知道的信息。
- 主动联系有存在感但不打扰；关闭后完全停止。
- 角色会合理拒绝、澄清和承认做不到，而不是伪造成功。
- 内部 AI 与 MCP 控制同一动作时，玩家能理解谁取得了控制权。
- 长任务的进度、取消、失败和恢复反馈清楚。
- 在线延迟、离线降级、重启恢复和长时间运行没有明显卡顿或状态漂移。
- GUI、多人体验和锁屏后无法自动完成的实机项目按验收清单逐项记录。

达到这一门槛后，本轮实现停止，不以继续增加框架功能替代真人证据。

## 后续规划

以下内容需要保留设计空间，但当前不实现。

### 角色质量

- 长期关系阶段、情绪余波、承诺与未完成话题。
- 有来源、可修正的角色观点，以及对矛盾记忆的显式处理。
- 模型提出新的安全小目标，再由 Host 或玩家批准。
- 可审计的 Canon 修订、遗忘、导出和玩家隐私删除。
- 更自然的多轮对话、语音打断、TTS 音色和无障碍字幕。
- 可配置的安静时段、每日主动上限、关系阶段和主动联系理由。
- 在线故障后的可选确定性自动降级；默认仍明确暴露故障，不静默改变人格能力。

### 动作与任务

- 更多物品、配方、工作站、交易、战斗辅助和复杂容器。
- 可验证的小型蓝图与分阶段建造，不接受无限制自然语言建筑。
- 多步骤规划的暂停、恢复、重新规划和资源预算。
- 多角色协作与冲突仲裁，前提是单角色闭环已有玩家价值。

### 引擎与 SDK

- 从 Control OpenAPI 生成跨语言 Host Control Client。
- 为 Godot、Unity、Unreal、Ren'Py 和其他服务端游戏提供同等深度的参考 Host。
- 将当前 Java 集成中通用的 Journal、Lease 和线程切换提炼为轻量 SDK。
- 保存格式和协议达到稳定门槛后再承诺 post-1.0 兼容。

### 控制与安全

- 多 Principal 配对、撤销、短期凭据和审计界面。
- MCP Streamable HTTP、TLS 和远程部署，仅在认证模型与威胁模型完成后开放。
- 多控制器 Lease、优先级、人工接管和仲裁。
- 官方 `2026-07-28` MCP 协议与 Go SDK 转为稳定发布后再移除 Preview 标记。
- 完整官方 Conformance 场景；当前门禁只运行与 Rin 暴露能力匹配的场景。

### 存储与性能

- 先测量 1,000、10,000 和 65,536 个 Operation 的延迟与写入量。
- 只有整文件快照超过实际预算时，才引入追加 Journal、分段文件或 SQLite/WAL。
- Read Model 持久化、压缩和归档必须有明确恢复语义，不能只做缓存。
- 大模型响应、语音和媒体继续存放在受限 Cache，不进入核心事件日志。

### 工具与内容生产

- 角色包、Capability 包和测试场景的静态校验工具。
- 受信任内容包的签名、版本回滚和来源记录。
- 面向作者的角色/任务预览器，但不在 Runtime 内执行任意脚本。
- 视觉小说或 RPG 可以使用相同 Canon 与控制端口，叙事热更新仍由具体游戏定义。

## 明确非目标

在当前产品证据不足前，不实现：

- 跨存档、跨服务器或跨游戏同步同一角色身份；
- 让模型直接取得世界写权限或绕过 Host Offer；
- 把 NPC 伪装成拥有完整客户端能力的真实玩家；
- 任意自然语言大型建筑、任意蓝图或任意脚本执行；
- 默认开启的多 Agent 辩论、群体自治或同时多控制器；
- 公网裸露的 MCP/Control API、托管账号系统或云同步；
- 为每种游戏和引擎承诺即插即用；
- 仅为追逐趋势引入向量数据库、ORM、消息队列或供应商 SDK。

## 复杂度预算

- 新依赖必须替代至少一段难以维护的自实现，并有许可证、跨平台和供应链理由。
- 新协议类型必须解决两个真实 Host 都无法用现有契约表达的问题。
- 新公共接口必须有调用方、失败语义、恢复语义和自动测试。
- 新专题文档必须有唯一读者任务；阶段计划只写在本文件。
- 生成文件必须可验证和可重建，不手工维护第二份契约。
- 没有玩家价值证据的实验放在示例或分支，不进入核心 Runtime。

## Preview 发布门槛

- Go、Adapter、SDK、契约和跨平台 Build 检查全部通过。
- 两份 OpenAPI、生成 Route Inventory 和叙述文档没有漂移。
- Fresh Clone 可以构建、测试并运行最小示例。
- MCP 使用官方 SDK，并通过当前能力对应的官方 Conformance 场景。
- 玩家价值主张不超过[已有证据](docs/player-value.zh-CN.md)。
- 真实 Fabric、BepInEx 和 Luanti 的人工验收结果被记录，未验证项明确标为 Preview。

核心原则不变：模型可以提出意图和表达，游戏引擎决定现实发生什么。

# Rin 路线图

[English](ROADMAP.en.md) | [简体中文](ROADMAP.md)

Rin 的方向是成为通用游戏 Agent Harness：模型在明确的负向约束内自由决定行动，
游戏 Adapter 保持世界权威，策略和 Operation 提供安全、恢复与审计。

当前源码是 `0.7.0` Preview。V2 采用破坏性演进，稳定性优先于旧接口兼容。

## 已完成基础

- 引擎无关的 Observation、Capability、Action、Effect、Epoch 和 Host Manifest。
- 基于实际 Effect 的确定性 Gameplay Policy、确认挑战、预算和安全内核。
- 常驻 Control Daemon、Host Lease、独占 Controller Lease、Emergency Stop。
- 可持久恢复的 Operation、Host 长轮询、取消、Run、Outcome 和执行证明。
- 外部 MCP 与内部 Agent 共用同一行动授权和执行链。
- 内部 Persona、Memory、Skill、结构化模型决策和异步 Agent Task API。
- Python、JavaScript、C#、Java、Lua Control SDK 与 Go HostKit。
- Grid、Story 和 Terminal 三个引擎中立验证 Adapter。
- 六种语言的通用 Host 契约脚手架与本地 MCP 一键安装/更新。
- 单二进制本地 Console、长目标入口、共享默认人格和公共记忆卡片管理。

## 当前门禁

1. 对 Rin 和首个真实游戏 Adapter 执行完整构建、Race、契约、MCP、SDK、安装器
   与凭据扫描。
2. 修复复扫发现的真实问题，不增加没有消费者的抽象。
3. 进入真人验收：长时间游玩、多人显式开放、GUI、急停、行为自然度与模型成本。

## 下一发布阶段

- 固化 `rin.host/v2` 与 `rin.control/v2` 的跨语言 Fixture 和 Adapter Conformance。
- 为 Host 断线、旧 Epoch、取消竞争、确认过期和 Outcome Unknown 增加故障注入。
- 给内部 Agent 增加可观察的记忆检索与压缩指标，不把全部历史塞入 Prompt。
- 增加 Persona/Memory/Skill Provider SPI 的独立实现示例，保持核心零外部服务依赖。
- 记录真实游戏中任务完成率、错误授权率、急停延迟、Token 成本和玩家干预率。
- 在真人结果稳定后再确定第一个 Release Candidate 和版本兼容承诺。

## 后续候选

这些方向只有在现有执行链通过真人验证后才进入实现：

- 同一 Actor 的可配置多 Controller 仲裁；
- 有签名身份、双向认证和部署审计的跨主机 Control Transport；
- 可替换的向量记忆 Provider，但继续保留内置轻量本地实现；
- 更丰富的宏任务恢复、子 Operation 可视化和跨 Adapter 评测套件；
- RPG、视觉小说、模拟经营等第二批完整参考 Adapter；
- 基于真人验收结果扩展当前 Console 的权限编辑与行为解释，不建立第二套控制面。

## 明确不做

- 不让模型逐帧模拟键鼠或直接调用引擎私有 API。
- 不执行模型生成的代码、Shell、脚本或任意原生调用。
- 不把 Minecraft 或任何单一游戏的数据类型放入核心契约。
- 不默认跨存档、跨服务器或跨游戏同步角色记忆。
- 不以大量文档、兼容层或抽象替代真实玩家价值验证。

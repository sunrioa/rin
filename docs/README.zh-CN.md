# Rin 文档

[English](README.md) | [简体中文](README.zh-CN.md)

Rin `0.7.0` 是 Preview、pre-1.0 软件。文档按公共接口组织，不以某个使用方
项目作为叙述中心，但 Preview minor 版本不提供 post-1.0 稳定性保证。

| 主题 | 简体中文 | English |
| --- | --- | --- |
| 权威 HTTP Wire Schema | [`api/openapi.json`](../api/openapi.json) | [`api/openapi.json`](../api/openapi.json) |
| 架构、存储与权威边界 | [架构](architecture.zh-CN.md) | [Architecture](architecture.md) |
| 引擎无关宿主与能力契约 | [宿主契约](host-contract.zh-CN.md) | [Host contract](host-contract.md) |
| 顺序、同时、Chance 与隐藏信息 | [OpenSpiel 验证](open-spiel-validation.zh-CN.md) | [OpenSpiel validation](open-spiel-validation.md) |
| 通用 Host 端口与 Coordinator | [通用 Host SDK](host-sdk.zh-CN.md) | [Host SDK](host-sdk.md) |
| Proposal、执行与恢复 | [Host 动作生命周期](action-lifecycle.zh-CN.md) | [Host action lifecycle](action-lifecycle.md) |
| HTTP 与状态契约 | [协议 v2](protocol-v2.zh-CN.md) | [Protocol v2](protocol-v2.md) |
| 在线模型配置 | [模型策略](model-policy.zh-CN.md) | [Model policy](model-policy.md) |
| 可选决策、记忆、语音与遥测端口 | [可选扩展端口](optional-extensions.zh-CN.md) | [Optional extensions](optional-extensions.md) |
| Ren'Py、Godot、Unity 与 Unreal | [游戏适配器](game-adapters.zh-CN.md) | [Game adapters](game-adapters.md) |
| 区域、任务与 NPC 动作 | [RPG 事件约定](rpg-events.zh-CN.md) | [RPG event conventions](rpg-events.md) |
| 跨语言客户端与 Mod | [SDK 与 Mod 套件](sdk-and-mods.zh-CN.md) | [SDK and mod kits](sdk-and-mods.md) |
| 离线 Host 项目生成器 | [通用 Host 脚手架](host-scaffolding.zh-CN.md) | [Host scaffolding](host-scaffolding.md) |
| 真实游戏稳定性与崩溃验收 | [真实宿主验收](host-integration-validation.zh-CN.md) | [Real-host validation](host-integration-validation.md) |
| 加速一年存储与生命周期验证 | [长会话验证](long-session-validation.zh-CN.md) | [Long-session validation](long-session-validation.md) |
| 宿主持久保证与分级 | [宿主持久保证分级](host-durability.zh-CN.md) | [Host durability profiles](host-durability.md) |
| 安全与漏洞报告 | [安全](../SECURITY.md) | [Security](../SECURITY.en.md) |
| 发布变化 | [变更日志](../CHANGELOG.zh-CN.md) | [Changelog](../CHANGELOG.md) |
| 发布与 Client 兼容 | [兼容矩阵](compatibility.zh-CN.md) | [Compatibility matrix](compatibility.md) |
| 已支持的可扩展 Session Transfer | [可扩展 Session Transfer](session-transfer.zh-CN.md) | [Scalable Session Transfer](session-transfer.md) |
| Session 生命周期、配额、删除与隐私 | [Session 生命周期](session-lifecycle.zh-CN.md) | [Session lifecycle](session-lifecycle.md) |
| 部署、Readiness、Diagnostics 与 Metrics | [部署与监控](operations.zh-CN.md) | [Deployment and monitoring](operations.md) |
| 可玩切片、实测价值与发布门禁 | [玩家价值证据](player-value.zh-CN.md) | [Player-value evidence](player-value.md) |
| 发布与不可变 Tag 流程 | [发布指南](release-guide.zh-CN.md) | [Release guide](release-guide.md) |
| 已交付里程碑与 Preview 门禁 | [路线图](../ROADMAP.md) | [Roadmap](../ROADMAP.en.md) |
| 仓库总览 | [项目说明](../README.md) | [README](../README.en.md) |

各语言 SDK 快速开始位于 [`sdk/`](../sdk/README.zh-CN.md)。使用
[`rin init host`](host-scaffolding.zh-CN.md) 可以生成自包含的 Fabric、BepInEx
或 Luanti 项目；规范源码模板仍位于 [`examples/mods/`](../examples/mods/)。

标准 [MIT License](../LICENSE) 英文原文是项目许可证；
依赖许可声明见 [`THIRD-PARTY-NOTICES.md`](../THIRD-PARTY-NOTICES.md)。

Path、Method、HTTP Status、必填字段与 JSON Shape 以 `api/openapi.json` 为准；
叙述文档定义事务与恢复语义。SDK Route Inventory 是生成的覆盖元数据，不是第二份
Wire Contract。

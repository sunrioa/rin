# Rin 文档

[English](README.md) | [简体中文](README.zh-CN.md)

Rin `0.6.0` 是 Preview、pre-1.0 软件。文档按公共接口组织，不以某个使用方
项目作为叙述中心，但 Preview minor 版本不提供 post-1.0 稳定性保证。

| 主题 | 简体中文 | English |
| --- | --- | --- |
| 权威 HTTP Wire Schema | [`api/openapi.json`](../api/openapi.json) | [`api/openapi.json`](../api/openapi.json) |
| 架构、存储与权威边界 | [架构](architecture.zh-CN.md) | [Architecture](architecture.md) |
| Proposal、应用与结果事务 | [动作结果记账](outcome-reporting.zh-CN.md) | [Action outcome reporting](outcome-reporting.md) |
| HTTP 与状态契约 | [协议 v1](protocol-v1.zh-CN.md) | [Protocol v1](protocol-v1.md) |
| 在线模型配置 | [模型策略](model-policy.zh-CN.md) | [Model policy](model-policy.md) |
| Ren'Py、Godot 与 Unity | [游戏适配器](game-adapters.zh-CN.md) | [Game adapters](game-adapters.md) |
| 区域、任务与 NPC 动作 | [RPG 事件约定](rpg-events.zh-CN.md) | [RPG event conventions](rpg-events.md) |
| 跨语言客户端与 Mod | [SDK 与 Mod 套件](sdk-and-mods.zh-CN.md) | [SDK and mod kits](sdk-and-mods.md) |
| 安全与漏洞报告 | [安全](../SECURITY.md) | [Security](../SECURITY.en.md) |
| 发布变化 | [变更日志](../CHANGELOG.zh-CN.md) | [Changelog](../CHANGELOG.md) |
| 发布与 Client 兼容 | [兼容矩阵](compatibility.zh-CN.md) | [Compatibility matrix](compatibility.md) |
| 从更早 Revision 升级 | [v0.6 迁移](migration-v0.6.zh-CN.md) | [v0.6 migration](migration-v0.6.md) |
| 发布与不可变 Tag 流程 | [发布指南](release-guide.zh-CN.md) | [Release guide](release-guide.md) |
| 已交付里程碑与 Preview 门禁 | [路线图](../ROADMAP.md) | [Roadmap](../ROADMAP.en.md) |
| 仓库总览 | [项目说明](../README.md) | [README](../README.en.md) |

各语言 SDK 快速开始位于 [`sdk/`](../sdk/README.zh-CN.md)。Fabric、
BepInEx 和 Luanti 安装模板位于 [`examples/mods/`](../examples/mods/)。

标准 [MIT License](../LICENSE) 英文原文是具有约束力的许可证文本。

Path、Method、HTTP Status、必填字段与 JSON Shape 以 `api/openapi.json` 为准；
叙述文档定义事务与恢复语义。SDK Route Inventory 是生成的覆盖元数据，不是第二份
Wire Contract。

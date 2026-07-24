# Rin Documentation

[English](README.md) | [简体中文](README.zh-CN.md)

Rin `0.6.0` is Preview, pre-1.0 software. Documentation is organized by public
interfaces rather than by individual consuming games, but Preview minor
releases do not carry a post-1.0 stability guarantee.

| Topic | English | 简体中文 |
| --- | --- | --- |
| Authoritative HTTP wire schema | [`api/openapi.json`](../api/openapi.json) | [`api/openapi.json`](../api/openapi.json) |
| Architecture, storage, and authority boundary | [Architecture](architecture.md) | [架构](architecture.zh-CN.md) |
| Proposal, application, and outcome transactions | [Action outcome reporting](outcome-reporting.md) | [动作结果记账](outcome-reporting.zh-CN.md) |
| HTTP and state contract | [Protocol v1](protocol-v1.md) | [协议 v1](protocol-v1.zh-CN.md) |
| Online-model configuration | [Model policy](model-policy.md) | [模型策略](model-policy.zh-CN.md) |
| Ren'Py, Godot, and Unity | [Game adapters](game-adapters.md) | [游戏适配器](game-adapters.zh-CN.md) |
| Regions, quests, and NPC actions | [RPG event conventions](rpg-events.md) | [RPG 事件约定](rpg-events.zh-CN.md) |
| Cross-language clients and mods | [SDK and mod kits](sdk-and-mods.md) | [SDK 与 Mod 套件](sdk-and-mods.zh-CN.md) |
| Security and reporting | [Security](../SECURITY.en.md) | [安全](../SECURITY.md) |
| Release changes | [Changelog](../CHANGELOG.md) | [变更日志](../CHANGELOG.zh-CN.md) |
| Release and client compatibility | [Compatibility matrix](compatibility.md) | [兼容矩阵](compatibility.zh-CN.md) |
| Upgrade from earlier revisions | [v0.6 migration](migration-v0.6.md) | [v0.6 迁移](migration-v0.6.zh-CN.md) |
| Release and immutable tag procedure | [Release guide](release-guide.md) | [发布指南](release-guide.zh-CN.md) |
| Delivered milestones and Preview gates | [Roadmap](../ROADMAP.en.md) | [路线图](../ROADMAP.md) |
| Repository overview | [README](../README.en.md) | [项目说明](../README.md) |

SDK-specific quick starts are under [`sdk/`](../sdk/README.md). Fabric,
BepInEx, and Luanti installation templates are under
[`examples/mods/`](../examples/mods/).

The standard [MIT License](../LICENSE) is the authoritative license text.

For paths, methods, HTTP statuses, required fields, and JSON shapes,
`api/openapi.json` is authoritative. Narrative documents define transaction
and recovery semantics. The SDK route inventory is generated coverage metadata,
not a second wire contract.

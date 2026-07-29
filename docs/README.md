# Rin Documentation

[English](README.md) | [简体中文](README.zh-CN.md)

Rin `0.7.0` is Preview, pre-1.0 software. Documentation is organized by public
interfaces rather than by individual consuming games, but Preview minor
releases do not carry a post-1.0 stability guarantee.

| Topic | English | 简体中文 |
| --- | --- | --- |
| Authoritative HTTP wire schema | [`api/openapi.json`](../api/openapi.json) | [`api/openapi.json`](../api/openapi.json) |
| Architecture, storage, and authority boundary | [Architecture](architecture.md) | [架构](architecture.zh-CN.md) |
| Engine-neutral host and capability contract | [Host contract](host-contract.md) | [宿主契约](host-contract.zh-CN.md) |
| Sequential, simultaneous, chance, and hidden information | [OpenSpiel validation](open-spiel-validation.md) | [OpenSpiel 验证](open-spiel-validation.zh-CN.md) |
| Universal Host ports and coordinator | [Host SDK](host-sdk.md) | [通用 Host SDK](host-sdk.zh-CN.md) |
| Proposal, execution, and recovery | [Host action lifecycle](action-lifecycle.md) | [Host 动作生命周期](action-lifecycle.zh-CN.md) |
| HTTP and state contract | [Protocol v2](protocol-v2.md) | [协议 v2](protocol-v2.zh-CN.md) |
| Online-model configuration | [Model policy](model-policy.md) | [模型策略](model-policy.zh-CN.md) |
| Optional decision, memory, speech, and telemetry ports | [Optional extensions](optional-extensions.md) | [可选扩展端口](optional-extensions.zh-CN.md) |
| MCP 2026-07-28 external control | [Quick start](mcp-control-plane.md) | [快速接入](mcp-control-plane.zh-CN.md) |
| MCP and Host Control Plane future phases | [Implementation plan](mcp-control-plane-plan.md) | [实施计划](mcp-control-plane-plan.zh-CN.md) |
| Ren'Py, Godot, Unity, and Unreal | [Game adapters](game-adapters.md) | [游戏适配器](game-adapters.zh-CN.md) |
| Regions, quests, and NPC actions | [RPG event conventions](rpg-events.md) | [RPG 事件约定](rpg-events.zh-CN.md) |
| Cross-language clients and mods | [SDK and mod kits](sdk-and-mods.md) | [SDK 与 Mod 套件](sdk-and-mods.zh-CN.md) |
| Offline Host project generator | [Host scaffolding](host-scaffolding.md) | [通用 Host 脚手架](host-scaffolding.zh-CN.md) |
| Real-game stability and crash validation | [Real-host validation](host-integration-validation.md) | [真实宿主验收](host-integration-validation.zh-CN.md) |
| Accelerated-year storage and lifecycle validation | [Long-session validation](long-session-validation.md) | [长会话验证](long-session-validation.zh-CN.md) |
| Host persistence guarantees and durability profiles | [Host durability profiles](host-durability.md) | [宿主持久保证分级](host-durability.zh-CN.md) |
| Security and reporting | [Security](../SECURITY.en.md) | [安全](../SECURITY.md) |
| Release changes | [Changelog](../CHANGELOG.md) | [变更日志](../CHANGELOG.zh-CN.md) |
| Release and client compatibility | [Compatibility matrix](compatibility.md) | [兼容矩阵](compatibility.zh-CN.md) |
| Supported scalable Session Transfer | [Scalable Session Transfer](session-transfer.md) | [可扩展 Session Transfer](session-transfer.zh-CN.md) |
| Session lifecycle, quotas, deletion, and privacy | [Session lifecycle](session-lifecycle.md) | [Session 生命周期](session-lifecycle.zh-CN.md) |
| Deployment, readiness, diagnostics, and metrics | [Deployment and monitoring](operations.md) | [部署与监控](operations.zh-CN.md) |
| Playable slice, measured value, and release gates | [Player-value evidence](player-value.md) | [玩家价值证据](player-value.zh-CN.md) |
| Release and immutable tag procedure | [Release guide](release-guide.md) | [发布指南](release-guide.zh-CN.md) |
| Delivered milestones and Preview gates | [Roadmap](../ROADMAP.en.md) | [路线图](../ROADMAP.md) |
| Repository overview | [README](../README.en.md) | [项目说明](../README.md) |

SDK-specific quick starts are under [`sdk/`](../sdk/README.md). Use
[`rin init host`](host-scaffolding.md) to generate a self-contained Fabric,
BepInEx, or Luanti project; the canonical source templates remain under
[`examples/mods/`](../examples/mods/).

The standard [MIT License](../LICENSE) is the authoritative project license;
dependency notices are recorded in
[`THIRD-PARTY-NOTICES.md`](../THIRD-PARTY-NOTICES.md).

For paths, methods, HTTP statuses, required fields, and JSON shapes,
`api/openapi.json` is authoritative. Narrative documents define transaction
and recovery semantics. The SDK route inventory is generated coverage metadata,
not a second wire contract.

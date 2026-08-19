# Rin 文档

[English](README.md) | [简体中文](README.zh-CN.md)

文档只描述当前 Harness V2。HTTP 字段和路由以 OpenAPI 为准，Go 公共类型以源码为准；
文档中的示例不能替代游戏 Adapter 的权威校验。

## 阅读顺序

1. [整体架构](architecture.zh-CN.md)：组件边界、信任关系与完整数据流。
2. [Host V2 契约](host-contract.zh-CN.md)：Observation、Capability、Action、Effect 与 Outcome。
3. [Operation 与策略](operations.zh-CN.md)：授权、确认、投递、恢复、取消和执行证明。
4. [游戏 Adapter](game-adapters.zh-CN.md)：具体引擎必须实现和禁止实现的内容。
5. [MCP 与 Control Plane](mcp-control-plane.zh-CN.md)：外部 Agent、Daemon、工具和安装更新。
6. [内部 Agent Runtime](internal-agent-runtime.zh-CN.md)：人格、记忆、Skill、模型和任务执行。
7. [Rin Console](console.zh-CN.md)：本地监控、长目标、共享人格和公共记忆卡片。
8. [任务时间线](task-timeline.zh-CN.md)：查看公开任务决策、策略、投递与权威结果。
9. [任务计划](task-plans.zh-CN.md)：在不绕过动作网关的前提下协调有界复杂任务。
10. [Signal 收件箱](signals.zh-CN.md)：由 Host 发布短期注意提示，供内部唤醒或外部 MCP 读取。
11. [Host 脚手架](host-scaffolding.zh-CN.md)：生成契约骨架并接入自己的语言与引擎。
12. [集成验收](host-integration-validation.zh-CN.md)：自动门禁与真人游戏测试。
13. [里程碑 A 验收报告](milestone-a-validation.zh-CN.md)：当前架构、跨 Adapter 证据、性能与
    仍需真人确认的项目。

补充资料：

- [SDK 总览](../sdk/README.zh-CN.md)
- [安全边界](../SECURITY.md)
- [路线图](../ROADMAP.md)

## 契约来源

| 契约 | 来源 |
| --- | --- |
| Host `rin.host/v2` | `host/*.go` |
| Control `rin.control/v2` | `api/control-openapi.json`、`controlplane/*.go` |
| Agent Task API `v1` | `api/agent-openapi.json`、`agentapi/*.go` |
| Task Timeline `v1` | `timeline/*.go`、`api/task-timeline-v1-fixtures.json` |
| Signal `rin.signal/v1` | `signalbox/*.go`、`api/signal-openapi.json` |
| MCP Tool | `mcpbridge/server.go` |
| Gameplay Policy | `policy/*.go` |

当前没有旧协议迁移、引擎专用模板或公共远程 Control 部署承诺。需要新增能力时，
先在具体游戏 Adapter 中证明玩家价值，再判断是否确实属于通用核心。

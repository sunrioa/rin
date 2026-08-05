# Rin

[简体中文](README.md) | [English](README.en.md)

Rin 是面向游戏的智能体运行时。它在游戏循环之外管理角色记忆、目标、决策和异步任务，并把结果作为经过校验的行动提案交回游戏。

## 当前状态

源码版本为 `0.7.0` Preview（pre-1.0），项目仍在开发中。正式 Release Tag 发布前，请固定仓库 Revision 或已验证的 Tag。兼容性和迁移信息见[兼容矩阵](docs/compatibility.zh-CN.md)与[变更日志](CHANGELOG.zh-CN.md)。

## Rin 负责什么

- 游戏提交角色确实观察到的 `Observation`；Rin 不读取也不解释整个游戏存档。
- 角色根据记忆、目标、边界和当前允许的动作生成 `ActionProposal`。
- 游戏保留世界权威，负责验证、执行或拒绝提案，再把动作结果报告给 Rin。
- 状态变化写入带哈希链的事件日志，可通过 Replay、Timeline 和 `rin inspect` 检查。
- 提案、Generation Job、快照和 Session Transfer 都有独立的大小、时间和并发上限。

Rin 可以作为 Sidecar 运行，也可以作为 Go 包嵌入工具链。它不绑定特定游戏、引擎或模型供应商。在线模型是可选的；没有模型时可以使用确定性 Policy。

## 快速开始

要求 Go 1.25 或更高版本。启动本地 Sidecar：

```bash
make test
go run ./cmd/rin serve -data ./rin-data
```

默认监听 `127.0.0.1:7374`。健康检查：

```bash
curl http://127.0.0.1:7374/health
```

运行最小示例：

```bash
go run ./examples/basic
```

该示例只演示 Session 创建和 Observe。带有 Proposal Attempt、崩溃恢复和 Outcome Outbox 的完整切片在 [`examples/terminal-story`](examples/terminal-story/README.zh-CN.md)。

构建常驻控制 daemon 和 MCP 薄代理（默认优先协商 `2026-07-28`）：

```bash
go build -o bin/rin ./cmd/rin
go build -o bin/rin-control ./cmd/rin-control
go build -o bin/rin-mcp ./cmd/rin-mcp
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read"
export RIN_CONTROL_DATA_DIR="/absolute/path/to/rin-control-data"
./bin/rin-control
```

把 Rin MCP 一次配置到本机已安装的 Codex、Claude Code 或 OpenClaw：

```bash
./bin/rin mcp install
./bin/rin mcp status
```

安装器交互选择 Agent，使用各 Agent 的官方 CLI 注册同一个稳定的 `rin-mcp`
路径，并把地址与 Token 只保存一次到权限为 `0600` 的本机配置。后续换入新版
Rin 发行目录后运行 `rin mcp update` 即可保留全部 Agent 配置。自动化环境可用
`-agents codex,claude,openclaw` 或 `-yes`；完整安装、更新与卸载说明见
[MCP 快速接入](docs/mcp-control-plane.zh-CN.md)。

`rin-control` 常驻监听 `127.0.0.1:7375`，游戏 Host 和任意数量的
`rin-mcp` STDIO 代理都连接它。默认 Scope 只有 `actor.read`；写工具必须显式
授权，并且所有世界修改仍由游戏 Host 最终校验和执行。配置、权限和 Host 端点见
[MCP 快速接入](docs/mcp-control-plane.zh-CN.md)。

生成 Host 或 Mod 起始项目：

```bash
go run ./cmd/rin init host --list-hosts
go run ./cmd/rin init host --engine fabric --id guide_npc --name "Guide NPC" --namespace io.github.example
```

`custom` 支持 Go、JavaScript、Python、C#、Java 和 Lua；另有 Fabric、BepInEx Mono、BepInEx IL2CPP 与 Luanti 模板。生成器不会覆盖已有路径，详见[Host 脚手架文档](docs/host-scaffolding.zh-CN.md)。

## 接入路径

- Ren'Py、Godot 4、Unity 和 Unreal 参考适配器
- Python、JavaScript、C#、Java 和 Lua SDK
- Fabric、BepInEx 和 Luanti 示例 Mod
- 引擎无关的 `host` Contract 与 HostKit

安装、线程边界和离线行为见[游戏适配文档](docs/game-adapters.zh-CN.md)。跨语言 SDK、凭据和 Mod 安装见 [SDK 与 Mod 文档](docs/sdk-and-mods.zh-CN.md)。

## 文档

- [文档索引](docs/README.zh-CN.md) / [English](docs/README.md)
- [总体流程图](docs/flowchart.zh-CN.md)：Runtime、Host、模型与 MCP 控制面的完整链路
- [Protocol v2](docs/protocol-v2.zh-CN.md)：字段、错误和重试语义
- [动作生命周期](docs/action-lifecycle.zh-CN.md)：Proposal、执行、Outbox 和恢复
- [MCP 快速接入](docs/mcp-control-plane.zh-CN.md)：官方版本协商、Host 发布和权限
- [部署与监控](docs/operations.zh-CN.md)：Token、TLS、存储和运行限制
- [发布指南](docs/release-guide.zh-CN.md)与[路线图](ROADMAP.md)
- [安全说明](SECURITY.md)、[变更日志](CHANGELOG.zh-CN.md)和[第三方许可](THIRD-PARTY-NOTICES.md)

`api/openapi.json` 是 Runtime HTTP 契约，`api/control-openapi.json` 是 Host
Control 契约；协议文档解释运行时语义，专题文档解释适配器、长期 Session、
Transfer 和可选扩展。根 README 不重复这些完整内容。

## 目录

```text
cmd/rin/       Sidecar 命令行程序
cmd/rin-control/ 常驻 Host Control daemon
cmd/rin-mcp/   MCP STDIO 薄代理
api/           Runtime 与 Control OpenAPI 3.1 契约
protocol/      跨语言 v2 数据类型
runtime/       事件状态机、提案验证、快照和调度
store/         JSONL 文件存储与内存存储
httpapi/       HTTP、鉴权和请求大小限制
controlplane/  Host 租约与主体隔离的控制面
mcpbridge/     官方 MCP SDK 与控制面的转换
sdk/           Python、JavaScript、C#、Java、Lua SDK
adapters/      Ren'Py 客户端与桥接
tools/         契约投影和验证工具
examples/      示例程序、适配器和 Mod
```

## 安全与部署

Rin 默认不访问网络。生产 Sidecar 应设置独立 Token，并让同机 TLS Reverse Proxy 终止远程连接：

```bash
export RIN_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/rin serve
```

远程监听必须同时声明 `-allow-remote`、至少 32 字节的 `RIN_TOKEN` 和 `-tls-proxy`（或 `RIN_TLS_PROXY=true`）。这些选项不会替代 TLS，也不会让公网明文监听变安全。Token、模型 Key 和供应商 URL 不会写入事件、快照或响应。完整边界见[部署与监控](docs/operations.zh-CN.md)和[安全说明](SECURITY.md)。

Rin 不负责渲染、导航、物理、战斗、背包、任务规则或任意脚本执行，也不把模型输出直接当作世界事实。项目不引入供应商 SDK、向量数据库、ORM、WebSocket 或动态插件执行。

## 许可证

Rin 以 [MIT License](LICENSE) 发布。

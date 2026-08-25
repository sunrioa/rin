# Rin SDK

[English](README.md) | [简体中文](README.zh-CN.md)

本目录提供面向 `rin.control/v2` 的轻量客户端，以及游戏 Host 使用的 Go
`hostkit`。所有语言客户端都连接常驻的本机 `rin-control`，不会嵌入模型、策略
或游戏执行逻辑。

| 语言 | 最低运行时 | 调用方式 | 说明 |
| --- | --- | --- | --- |
| Python | 3.9 | 同步 | 仅标准库 |
| JavaScript | Node 18 | Promise | 使用标准 `fetch` |
| C# | .NET 6 / .NET Standard 2.0 | `Task` | 严格有界 HTTP |
| Java | 17 | `CompletableFuture` | JSON Codec 由宿主注入 |
| Lua | 5.1+ | Callback | HTTP 与 JSON Adapter 由引擎注入 |
| Go HostKit | Go 1.25 | `context.Context` | Authority Dispatch 与 V2 Adapter 协调 |

## 共享 Control 子集

五种语言客户端都实现以下面向调用方的 Control 子集。额外的 Host、Signal、Agent
或任务计划认证接口因语言而异，以各语言指南为准。

- 读取世界、Actor、Observation 和 Capability；
- 获取、续租和释放独占 Controller Lease；
- 提交或确认 `ActionRequest`；
- 获取、长轮询和取消 Operation；
- 获取或长轮询共用的 `rin.task-timeline/v1` 公开证据流；
- 设置 Actor Emergency Stop。

精确字段以 [`api/control-openapi.json`](../api/control-openapi.json) 为准。SDK
刻意使用通用 JSON Object，避免复制并逐渐漂移出第二套协议类型。

## 传输保证

所有客户端都执行以下检查：

- 默认只连接 `http://127.0.0.1:7375` 或显式回环地址；
- 要求至少 32 字节、无换行的 Bearer Token；
- 禁止 HTTP Redirect；
- 限制超时和响应体大小；
- 限制请求 Payload 的 JSON 深度和安全整数；
- 拒绝非法 UTF-8、非 JSON 响应和不匹配的 `rin.control/v2`；
- 返回稳定错误码和有界错误记录；具体异常类型或回调形式因语言而异。

Daemon 启动与 Bearer Token 处理以 [MCP 与 Host Control 快速接入](../docs/mcp-control-plane.zh-CN.md)
为准；等待、超时、终态和权威 Outcome 以 [Operation 语义](../docs/operations.zh-CN.md)
为准。

SDK 返回的 `queued`、`accepted` 或 `running` 只表示中间状态。调用方必须等待
终态，并且只有 `execution_confirmed=true` 且存在 Host Outcome 时才可向用户报告
游戏行动已经完成。

## 版本与发布

这些 SDK 当前是仓库内的 source-first Preview。固定同一个 Rin Revision 使用，
不要假设公共包仓库中的同名包已经发布或与源码同步。

各语言快速开始：

- [Python](python/README.zh-CN.md)
- [JavaScript](javascript/README.zh-CN.md)
- [C#](csharp/README.zh-CN.md)
- [Java](java/README.zh-CN.md)
- [Lua](lua/README.zh-CN.md)

完整 Host 接入见[游戏 Adapter 指南](../docs/game-adapters.zh-CN.md)。

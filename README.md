# Rin

Rin 是一个面向游戏角色的轻量级 Agent Runtime。它作为游戏进程旁边的 Sidecar 运行，也可以直接作为 Go 包嵌入工具链。核心只使用 Go 标准库，不绑定视觉小说、RPG 引擎或任何模型供应商。

当前版本：`v0.1.0`（首个可运行原型）

## 它解决什么

Rin 将“角色思考”和“游戏世界事实”拆开：

- 游戏提交角色实际看见的 `Observation`，而不是把整个存档交给模型。
- 角色根据记忆、目标、边界和当前允许动作生成 `ActionProposal`。
- 提案不能直接改变剧情、背包、任务或关系；游戏验证并调用 `commit` 后才生效。
- 每次状态变化写入带哈希链的 JSONL 事件日志，可重放、可检查。
- 快照绑定 `game/content/version/hash`，篡改或串档会被拒绝。
- 多 NPC 通过 tick 调度按需思考，不需要每帧调用模型。

这套边界既适用于 Ren'Py 角色，也可用于 RPG NPC、队友、经营模拟居民和其他 AI 游戏实体。

## 快速开始

要求 Go 1.24 或更高版本。

```bash
go test ./...
go run ./cmd/rin serve -data ./rin-data
```

默认监听 `127.0.0.1:7374`。检查服务：

```bash
curl http://127.0.0.1:7374/health
```

运行完整客户端示例：

```bash
go run ./examples/basic
```

生产接入建议设置独立 Sidecar Token：

```bash
export RIN_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/rin serve
```

客户端随后发送 `Authorization: Bearer $RIN_TOKEN`。Token、模型 API Key 和供应商配置均不会写入事件、快照或响应。

## API

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/health` | 无鉴权健康检查 |
| `POST` | `/v1/session/create` | 创建绑定游戏内容版本的会话 |
| `POST` | `/v1/session/observe` | 提交一个或多个角色确实观察到的事件 |
| `POST` | `/v1/agent/propose` | 从游戏白名单动作中产生角色提案 |
| `POST` | `/v1/action/commit` | 接受或拒绝提案并记录结果 |
| `POST` | `/v1/scheduler/due` | 查询当前 tick 应思考的角色 |
| `POST` | `/v1/session/get` | 读取会话状态 |
| `POST` | `/v1/session/snapshot` | 创建并原子保存快照 |
| `POST` | `/v1/session/restore` | 校验并恢复快照 |

所有写请求都带调用方生成的 `request_id`，重复请求返回相同结果，不重复修改状态。同一 ID 被用于不同操作时返回冲突。

完整字段和错误语义见 [协议文档](docs/protocol-v1.md)，职责边界见 [架构文档](docs/architecture.md)。

## 目录

```text
cmd/rin/       Sidecar 命令行程序
httpapi/       严格 JSON、鉴权、请求大小限制
policy/        零网络依赖的确定性离线策略
protocol/      可跨语言实现的 v1 数据契约
runtime/       事件状态机、提案验证、快照和调度
store/         JSONL 文件存储与内存存储
examples/      最小接入示例
```

## 当前有意不做

`v0.1.0` 不内置大模型 SDK、向量数据库、ORM、WebSocket、动态插件执行或任意文件访问。模型接入将作为可选 Policy；即使供应商不可用，游戏仍可继续使用确定性策略或自己的离线剧情。

后续工作记录在 [ROADMAP.md](ROADMAP.md)。

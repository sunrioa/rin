# Rin

Rin 是一个面向游戏角色的轻量级 Agent Runtime。它作为游戏进程旁边的 Sidecar 运行，也可以直接作为 Go 包嵌入工具链。核心只使用 Go 标准库，不绑定视觉小说、RPG 引擎或任何模型供应商。

当前开发线：`v0.5.0`（Living Worlds）

## 它解决什么

Rin 将“角色思考”和“游戏世界事实”拆开：

- 游戏提交角色实际看见的 `Observation`，而不是把整个存档交给模型。
- 角色根据记忆、目标、边界和当前允许动作生成 `ActionProposal`。
- 提案不能直接改变剧情、背包、任务或关系；游戏验证并调用 `commit` 后才生效。
- 每次状态变化写入带哈希链的 JSONL 事件日志，可重放、可检查。
- 快照绑定 `game/content/version/hash`，篡改或串档会被拒绝。
- 多 NPC 通过 tick 调度按需思考，不需要每帧调用模型。
- 在线模型通过异步 Job 预取，慢请求、取消和状态过期不会冻结游戏主线程。
- 通用结构化 Generation Job 让剧情、任务描述和受限对白也经过 Sidecar，而不是让游戏保存供应商 Key。
- 模型不可用时自动回退确定性 Policy，并用 `policy_source` 标明来源。
- Ren'Py、Godot 4 和 Unity 适配器保持同一套 observe / propose / commit 权威边界。
- 可选分层记忆、冲突认知、候选小目标、区域休眠和确定性多角色仲裁均由 Session feature 显式启用。
- 脱敏 Timeline、指定 revision Replay 和 `rin inspect` 让长流程角色行为可以复现和审计。

这套边界既适用于 Ren'Py 角色，也可用于 RPG NPC、队友、经营模拟居民和其他 AI 游戏实体。

## 快速开始

运行 Sidecar 要求 Go 1.24 或更高版本；执行 Ren'Py 适配器测试还需要 Python 3.9+。

```bash
make test
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

客户端随后发送 `Authorization: Bearer $RIN_TOKEN`。Token、模型 API Key 和供应商 URL 均不会写入事件、快照或响应；Generation 结果只可带有经过长度限制的模型名、结束原因和 token 计数等非秘密运维元数据，游戏可按自己的持久化白名单继续过滤。

## API

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/health` | 无鉴权健康检查 |
| `POST` | `/v1/session/create` | 创建绑定游戏内容版本的会话 |
| `POST` | `/v1/session/observe` | 提交一个或多个角色确实观察到的事件 |
| `POST` | `/v1/agent/propose` | 从游戏白名单动作中产生角色提案 |
| `POST` | `/v1/jobs/propose` | 异步提交角色提案任务 |
| `GET` | `/v1/jobs/{job_id}` | 查询任务状态与结果 |
| `DELETE` | `/v1/jobs/{job_id}` | 取消排队或执行中的任务 |
| `POST` | `/v1/generation/jobs` | 异步提交结构化 JSON 生成任务 |
| `GET` | `/v1/generation/jobs/{job_id}` | 查询生成任务与安全元数据 |
| `DELETE` | `/v1/generation/jobs/{job_id}` | 取消生成任务 |
| `POST` | `/v1/action/commit` | 接受或拒绝提案并记录结果 |
| `POST` | `/v1/action/commit-batch` | 原子提交同一世界版本的多角色结果 |
| `POST` | `/v1/session/activity` | 更新角色区域与 awake/dormant 状态 |
| `POST` | `/v1/world/arbitrate` | 对并行角色提案进行确定性冲突仲裁 |
| `POST` | `/v1/scheduler/due` | 查询当前 tick 应思考的角色 |
| `POST` | `/v1/session/get` | 读取会话状态 |
| `POST` | `/v1/session/snapshot` | 创建并原子保存快照 |
| `POST` | `/v1/session/restore` | 校验并恢复快照 |
| `POST` | `/v1/session/timeline` | 读取脱敏事件时间线 |
| `POST` | `/v1/session/replay` | 重放到指定 revision 并返回 Snapshot |

所有写请求都带调用方生成的 `request_id`，重复请求返回相同结果，不重复修改状态。同一 ID 被用于不同操作时返回冲突。

完整字段和错误语义见 [协议文档](docs/protocol-v1.md)，职责边界见 [架构文档](docs/architecture.md)。

离线检查一个会话（会验证日志并只打印脱敏时间线）：

```bash
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1 -revision 42
```

## 游戏引擎适配

- Ren'Py：纯标准库 Python 客户端、`renpy.invoke_in_thread` 桥接与 authored 离线回退。
- Godot 4：基于 `HTTPRequest` signal/timer 的异步客户端。
- Unity：基于 `UnityWebRequest` coroutine 的异步客户端和有界响应处理。

安装、配置和离线语义见 [游戏适配文档](docs/game-adapters.md)。RPG 的区域、可见性、任务和多人 NPC 事件约定见 [RPG 事件约定](docs/rpg-events.md)。

## 可选模型 Policy

默认不访问网络。启用 OpenAI-compatible 模型：

```bash
export RIN_POLICY=model
export RIN_MODEL_BASE_URL="https://provider.example/v1"
export RIN_MODEL="your-model-id"
export RIN_MODEL_API_KEY="..."
go run ./cmd/rin serve
```

远程端点必须使用 HTTPS；本机 `127.0.0.1`、`::1`、`localhost` 模型可使用 HTTP 且可不配置 Key。模型调用具有独立超时、总预算、有限重试、熔断和有界缓存。详细配置见 [模型接入文档](docs/model-policy.md)。

## 目录

```text
cmd/rin/       Sidecar 命令行程序
httpapi/       严格 JSON、鉴权、请求大小限制
policy/        零网络依赖的确定性离线策略
provider/      OpenAI-compatible 客户端、重试与熔断
jobs/          有界异步 Proposal worker queue
generation/    有界结构化 Generation worker queue 与缓存
adapters/      Ren'Py Python 客户端与桥接层
compat/        可执行的游戏协议兼容向量
protocol/      可跨语言实现的 v1 数据契约
runtime/       事件状态机、提案验证、快照和调度
store/         JSONL 文件存储与内存存储
examples/      Go、Godot 与 Unity 最小接入示例
```

## 当前有意不做

`v0.5.0` 不引入供应商 SDK、向量数据库、ORM、WebSocket、动态插件执行或任意文件访问。在线模型仍是可选能力；即使供应商或 Sidecar 不可用，游戏仍可继续使用确定性策略或自己的离线剧情。

后续工作记录在 [ROADMAP.md](ROADMAP.md)。

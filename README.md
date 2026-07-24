# Rin

[简体中文](README.md) | [English](README.en.md)

> 面向游戏的智能体运行时。

Rin 在游戏循环之外管理角色记忆、目标、决策、异步模型任务和可验证回放。
游戏始终保留世界权威，只接收经过本地约束验证的行动提案。Rin 可以作为
Sidecar 运行，也可以作为 Go 包嵌入工具链；核心只使用 Go 标准库，不绑定
具体游戏、引擎或模型供应商。

文档索引：[简体中文](docs/README.zh-CN.md) | [English](docs/README.md)

## 核心能力

Rin 将“角色思考”和“游戏世界事实”拆开：

- 游戏提交角色实际看见的 `Observation`，而不是把整个存档交给模型。
- 角色根据记忆、目标、边界和当前允许动作生成 `ActionProposal`。
- 提案不能直接改变剧情、背包、任务或关系；游戏验证并应用或拒绝后，
  用 `commit` 向 Rin 回报实际结果。
- 每次状态变化写入带哈希链的 JSONL 事件日志，可重放、可检查。
- 快照绑定 `game/content/version/hash`；其 SHA-256 canonical checksum 可发现
  意外损坏或未同步修改，Restore 则拒绝 Binding 不匹配。
- 多 NPC 通过 tick 调度按需思考，不需要每帧调用模型。
- 在线模型通过异步 Job 预取，慢请求、取消和状态过期不会冻结游戏主线程。
- 通用结构化 Generation Job 让剧情、任务描述和受限对白也经过 Sidecar，而不是让游戏保存供应商 Key。
- 模型不可用时自动回退确定性 Policy，并用 `policy_source` 标明来源。
- “游戏先处理、再回报”以及延迟结果合并要求新 Session 显式请求
  `outcome-reporting-v1`；未启用的 Session 为保持重放兼容，继续使用旧版
  Commit/stale 语义。
- Ren'Py、Godot 4 和 Unity 适配器保持同一套 observe / propose / commit 权威边界。
- Python、JavaScript、C#、Java、Lua SDK 与 Fabric、BepInEx、Luanti 示例 Mod 提供快速接入层。
- 可选分层记忆、冲突认知、候选小目标、区域休眠和确定性多角色仲裁均由 Session feature 显式启用。
- 脱敏 Timeline、指定 revision Replay 和 `rin inspect` 让长流程角色行为可以复现和审计。

同一套边界可以服务叙事角色、RPG NPC、队友、模拟居民和服务器实体。

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
| `POST` | `/v1/action/commit` | 记录游戏已经应用或拒绝的实际结果 |
| `POST` | `/v1/action/commit-batch` | 原子记录同一原始世界版本的多角色结果 |
| `POST` | `/v1/session/activity` | 更新角色区域与 awake/dormant 状态 |
| `POST` | `/v1/world/arbitrate` | 对并行角色提案进行确定性冲突仲裁 |
| `POST` | `/v1/scheduler/due` | 查询当前 tick 应思考的角色 |
| `POST` | `/v1/session/get` | 读取会话状态 |
| `POST` | `/v1/session/snapshot` | 创建并原子保存快照 |
| `POST` | `/v1/session/restore` | 校验并恢复快照 |
| `POST` | `/v1/session/timeline` | 读取脱敏事件时间线 |
| `POST` | `/v1/session/replay` | 重放到指定 revision 并返回 Snapshot |

每个持久 Session mutation 都带调用方生成的 `request_id`。在同一 Session
lineage 内，Rin 会把该 ID 永久绑定到 mutation 类型、canonical typed JSON
payload 和首次持久结果。完全相同的重试不会修改状态，而是返回首次结果的
revision/head（或原始 Proposal/Arbitration），并设置 `duplicate=true`；同一 ID
用于不同操作或 payload 时返回 `409 request_id_conflict`。Observe、Commit 与
Batch 的每个 Item 共用一个永久、Session-scoped 的 `event_id` 命名空间。有界
State Receipt 只是这份历史的热投影。

如果 Rin 无法确认非 Proposal mutation 是否已经写入持久 Store，会返回
`mutation_outcome_unknown`。调用方必须保留原 Operation，并以完全相同的类型、
payload 和 ID 重试；在同一 Request ID 下改变请求会返回
`request_id_conflict`，其他 Session mutation 则会被这条未决 tail 阻塞。
Proposal 写入继续使用兼容错误码 `proposal_outcome_unknown`，恢复规则相同。

Proposal 与 Generation Job 记录采用独立的、有界进程内保留策略。特别是
Generation Job 被淘汰或 Sidecar 重启后，同一请求可能再次执行；持久 Session
mutation 的保证不适用于 Generation Job。

Snapshot hash 是 checksum，不是签名或来源证明：能修改 Snapshot 的一方也能
重新计算 hash。应把 Snapshot 当作可信、不透明的状态，并按事件日志同等级别
保护。Restore 必须携带来自运行中游戏可信内容 manifest 的
`expected_binding`；它必须与 `snapshot.state.binding` 一致，目标 Session
已存在时还必须与该 Session 的 Binding 一致。

Inline Snapshot 的 compact JSON 上限为 16 MiB；超限时 Rin 返回
`413 snapshot_too_large`，绝不截断 Snapshot。服务端默认请求正文上限与所有
随附客户端默认响应上限均为 32 MiB，为 API envelope、Restore 元数据和持久
EventRecord framing 预留空间。当前不提供流式 Snapshot 传输，因此 lineage
超过 inline 上限后，不能通过这些 JSON endpoint 导出、Replay 或 Restore。

完整字段和错误语义见 [协议文档](docs/protocol-v1.zh-CN.md)，职责边界见
[架构文档](docs/architecture.zh-CN.md)，应用、结果记账和重试顺序见
[动作结果记账](docs/outcome-reporting.zh-CN.md)。

离线检查一个会话（会校验请求所经过的恢复路径，并只打印脱敏时间线；健康
revision index 会直接定位请求的尾部窗口，不会从 genesis 向前分页）：

```bash
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1 -revision 42
```

随附 File Store 会持有数据目录的 non-blocking exclusive lock，因此运行
`rin inspect` 或进行未协调的文件系统备份前，应先停止 Sidecar。嵌入式 Go
调用方必须调用 `(*store.File).Close()` 释放该锁。Engine 启动时只 lazy 枚举
Session；第一次访问会从最新可用且已校验的内部 checkpoint 加载，若无可用
checkpoint 则从 genesis 开始，再重放 event tail。lazy recovery 没有使用
checkpoint，或 `head revision / 所选 checkpoint revision >= 2` 时，Runtime
会 best-effort 异步排队一个恢复出的 head checkpoint；read 返回时它可能尚未
持久化，缓存写入失败也不会让 read 失败。运维需要不依赖 checkpoint、从
genesis 到 head 审计所有 Session 时，调用 `Engine.VerifyAll()`。

随附 `flock` 实现当前只支持 `darwin` 与 `linux`。其他所有 GOOS 上，
`store.OpenFile` 会返回 `ErrDataDirectoryLockUnsupported` 并 fail closed，
不会返回可用的 File Store。

随附 File Store 只能用于 `flock`、同目录原子 rename、file `fsync` 与 directory
`fsync` 语义可靠的本地文件系统。不支持 NFS、SMB、FUSE mount 和云同步目录；
远程或共享存储必须使用外部协调的 Store。

事件日志采用 `retain_forever`，因为 Replay、持久 Identifier History 与审计
依赖它。File Store 默认保留每个 Session 最近 2 个有效内部 checkpoint 和最近
2 个有效公共 Snapshot 文件。容量规划与备份必须计入无限增长的事件日志与
Identifier History；Rin 不提供事件日志自动归档，也不提供流式 Snapshot 传输。

## 游戏引擎适配

- Ren'Py：纯标准库 Python 客户端、`renpy.invoke_in_thread` 桥接与 authored 离线回退。
- Godot 4：基于 `HTTPRequest` signal/timer 的异步客户端。
- Unity：基于 `UnityWebRequest` coroutine 的异步客户端和有界响应处理。
- 通用 SDK：Python 3.9+、Node/Fetch、.NET 6+、Java 17+ 与 Lua 5.1+。
- 示例 Mod：Fabric 服务端、BepInEx 6 与本机 Sidecar 限定的 Luanti 服务端 Mod。

安装、配置和离线语义见 [游戏适配文档](docs/game-adapters.zh-CN.md)。RPG 的区域、可见性、任务和多人 NPC 事件约定见 [RPG 事件约定](docs/rpg-events.zh-CN.md)。
跨语言目录规范、线程边界、凭据策略和 Mod 安装步骤见 [SDK 与 Mod 接入文档](docs/sdk-and-mods.zh-CN.md)。

## 可选模型 Policy

默认不访问网络。启用 OpenAI-compatible 模型：

```bash
export RIN_POLICY=model
export RIN_MODEL_BASE_URL="https://provider.example/v1"
export RIN_MODEL="your-model-id"
export RIN_MODEL_API_KEY="..."
go run ./cmd/rin serve
```

远程端点必须使用 HTTPS；本机 `127.0.0.1`、`::1`、`localhost` 模型可使用 HTTP 且可不配置 Key。模型调用具有独立超时、总预算、有限重试、熔断和有界缓存。详细配置见 [模型接入文档](docs/model-policy.zh-CN.md)。

## 目录

```text
cmd/rin/       Sidecar 命令行程序
httpapi/       严格 JSON、鉴权、请求大小限制
policy/        零网络依赖的确定性离线策略
provider/      OpenAI-compatible 客户端、重试与熔断
jobs/          有界异步 Proposal worker queue
generation/    有界结构化 Generation worker queue 与缓存
adapters/      Ren'Py Python 客户端与桥接层
sdk/           Python、JavaScript、C#、Java、Lua 通用客户端与路由契约
compat/        可执行的游戏协议兼容向量
protocol/      可跨语言实现的 v1 数据契约
runtime/       事件状态机、提案验证、快照和调度
store/         JSONL 文件存储与内存存储
examples/      Go、Godot、Unity 与 Fabric/BepInEx/Luanti Mod 示例
```

## 能力边界

Rin 不负责渲染、导航、物理、战斗、背包、任务规则或任意脚本执行，也不把
模型输出直接当作世界事实。项目不引入供应商 SDK、向量数据库、ORM、
WebSocket、动态插件执行或任意文件访问。在线模型始终是可选能力；供应商或
Sidecar 不可用时，游戏仍可使用确定性策略或自己的离线内容。

后续工作记录在 [ROADMAP.md](ROADMAP.md)。

## 许可证

Rin 以 [MIT License](LICENSE) 发布。

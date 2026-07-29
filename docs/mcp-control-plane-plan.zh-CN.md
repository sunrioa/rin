# MCP 外部控制与 Host Control Plane 实施计划

[English](mcp-control-plane-plan.md) | [简体中文]

状态：规划中，尚未作为 Rin `0.7.0` 的受支持能力发布。

本文定义 Rin 如何让 Codex、Claude Code 和其他 MCP Client 安全控制游戏内 Actor，
同时保证 MCP、游戏内命令和游戏内 AI 最终进入同一条 Host 执行链路。Minecraft、
RPG、视觉小说或其他引擎的具体世界模型继续留在各自 Adapter 中。

## 目标

- 提供与游戏类型无关的 Actor 发现、对话、指令、Offer 执行和 Operation 管理。
- 让 MCP 与游戏内 API 复用相同的权限检查、Epoch、主线程执行、存档和 Outcome。
- 支持本地 STDIO 与回环 Streamable HTTP MCP Client。
- 让 NPC 保留拒绝、协商和自主决定的空间，而不是把所有外部请求都当作强制命令。
- 保持 Rin Core 不依赖具体游戏、Agent 框架或模型供应商。
- 对重试、断线、重启、迟到结果和部分完成的长任务给出明确语义。

## 非目标

- MCP Server 不提供 Shell、控制台、任意脚本或任意游戏 API 反射。
- MCP Tool 不自行生成坐标、对象引用、物品 ID、NBT 或未授权 Capability 参数。
- 不通过屏幕识别和键鼠模拟宣称 Host Contract 一致性。
- 第一版不提供公网多租户控制、云端中继或跨服务器 Actor 迁移。
- 第一版不允许外部 Client 绕过 NPC 性格、服务器规则或玩家确认。

## 先实现什么

实施顺序必须是：

1. **Host Control Plane 契约与只读状态。**
2. **只读 MCP Gateway。**
3. **有界 Directive 与精确 Offer 执行。**
4. **第一个真实 Host Adapter 的端到端接入。**
5. **更多游戏能力、长任务和多 Actor 协作。**

不能先把 MCP Tool 直接连到某个 Mod 的控制器。当前 Rin 主要处理
`Host -> Rin -> Policy`，而外部控制需要补充 `MCP -> Rin -> Host` 的反向通道。
先建立 Control Plane，才能避免每个引擎重复实现鉴权、排队、幂等与结果查询。

第一条可玩垂直切片应只包含：

- 列出 Host、World 和 Actor；
- 读取 Actor 的脱敏状态和当前 Offer；
- 向 Actor 发送一句话或一条可拒绝的 Directive；
- 按精确 `offer_id` 执行一个仍然有效的 Host-authored Offer；
- 查询 Operation 直到终态。

## 目标架构

```text
Codex / Claude Code / other MCP clients
                |
       STDIO or Streamable HTTP
                v
          cmd/rin-mcp
                |
       authenticated local API
                v
       Rin Host Control Plane
       - host lease and heartbeat
       - actor/read model registry
       - bounded command queue
       - operation/event journal
       - scope and audit policy
                ^
       host poll / ack / report
                |
Minecraft / Ren'Py / Godot / Unity / custom engine adapter
                |
       authoritative game thread
```

Host 主动向 Rin 注册并长轮询控制队列。游戏不需要开放额外入站端口，MCP Client
也不会持有游戏进程对象。所有世界修改仍由 Host 在权威线程完成。

## 同效果不变量

无论请求来自游戏 UI、聊天命令、游戏内 AI 还是 MCP，都必须满足：

1. 进入同一个 Host `ControlService` 或等价应用服务；
2. 使用同一份可信 Principal、Scope 和所有权状态；
3. 使用同一份当前 Epoch、Offer、Capability Descriptor 和 Deadline；
4. 在执行前进行同一组 TOCTOU 复验；
5. 只在游戏权威线程解析 `HostRef` 并修改世界；
6. 使用同一 Operation ID、Applied Marker、存档和 Outcome Outbox；
7. 产生相同的世界事件、NPC 记忆输入和审计结果；
8. 失败时返回结构化原因，不能悄悄降级为另一项世界操作。

“效果相同”表示相同合法输入进入相同执行器并产生相同权威结果，不表示不同模型一定
做出相同选择，也不表示 MCP Client 自动获得管理员权限。

## Control Plane 契约

### 核心对象

| 对象 | 作用 |
| --- | --- |
| `HostRegistration` | 声明 Host、协议版本、Manifest、World 和支持的控制能力 |
| `HostLease` | 有期限的连接所有权；断开或超时后阻止新写操作 |
| `ActorSnapshot` | 脱敏的 Actor 状态、位置语义、活动任务和可见关系 |
| `OfferSnapshot` | Host 当前明确授权的 Offer 集合及其 Epoch、Digest 和期限 |
| `ControlRequest` | 对话、Directive、Offer 执行或取消请求 |
| `ControlOperation` | 可重试、可查询的排队或执行状态 |
| `ControlEvent` | 单调序号的进度、结果和失效事件 |
| `ControlOutcome` | Host 观察到的终态结果与有限证据 |

所有 ID 都是不透明字符串。Principal、Scope、Host ID 和 World ID 不能由 MCP Tool
参数自行声明；它们来自配对会话和 Host 注册状态。

### Operation 状态

```text
submitted -> queued -> delivered -> accepted -> running -> succeeded
                        |            |          |-> failed
                        |            |          |-> cancelled
                        |            |          |-> interrupted
                        |            |          `-> outcome-unknown
                        |            `-> rejected
                        `-> stale
```

- `request_id` 用于 MCP 重试幂等，重复提交返回同一 Operation。
- Host 必须先持久保存请求接收状态，再回 ACK。
- Rin 必须先持久保存终态 Outcome，再从待确认队列删除。
- Lease 失效时，未投递请求进入 `stale`；已执行请求只能等待 Host 对账。
- Cancel 是请求，不代表回滚。是否可取消由 Capability Descriptor 决定。

### 建议的 Host API

精确 Path 和 JSON Shape 在实现阶段进入 OpenAPI 3.1，叙述计划不作为 Wire Contract。
需要覆盖以下语义：

- Host 注册、续租、心跳和注销；
- 发布 World、Actor、Actor Snapshot 与 Offer Snapshot；
- 长轮询领取有界 Control Request；
- ACK、进度、终态 Outcome 和恢复对账；
- 外部 Principal 可见 Host、Actor、Offer 和 Operation 的查询；
- 撤销配对或 Scope 后立即阻止新投递。

## MCP Gateway

### 稳定 Tool 集

第一版使用少量通用 Tool，不为每个游戏 Capability 动态生成一个 MCP Tool：

| Tool | 最低 Scope | 语义 |
| --- | --- | --- |
| `list_worlds` | `actor.read` | 列出 Principal 可见的在线 World |
| `list_actors` | `actor.read` | 按 World 列出可见 Actor |
| `get_actor_state` | `actor.read` | 读取脱敏 Snapshot 和活跃 Operation |
| `list_actor_offers` | `actor.read` | 读取当前未过期 Offer |
| `send_actor_message` | `actor.converse` | 发送对话，不直接产生世界效果 |
| `send_actor_directive` | `actor.direct` | 提交可拒绝、可协商的目标 |
| `execute_actor_offer` | `actor.execute` | 选择精确的当前 `offer_id` |
| `get_operation` | 对应操作 Scope | 查询状态和结构化结果 |
| `cancel_operation` | 对应操作 Scope | 请求取消可取消的 Operation |

`send_actor_directive` 应当是默认写入口。它表达“希望 Actor 完成什么”，Host 和 NPC
可以拒绝、询问或拆分任务。`execute_actor_offer` 权限更高，但参数只能引用 Host
刚刚发布的完整绑定 Offer，不能提交任意方法名或参数 JSON。

### Transport

- MCP Wire 使用官方 SDK 的默认版本协商，优先选择 `2026-07-28`，并兼容 SDK
  支持的旧版协议。
- 使用官方 Go MCP SDK `v1.7.0-pre.3`，直到支持 0728 的稳定版发布并通过回归。
- 0728 使用 `server/discover` 和每请求标准 `_meta`；旧版继续使用
  `initialize` 会话握手。
- Streamable HTTP 使用 0728 无状态模式，只绑定 `127.0.0.1`/`::1`。
- HTTP 必须验证 `Origin`、要求随机 Bearer Token，并限制请求体、并发和空闲时间。
- 使用官方 Go MCP SDK；MCP SDK 类型只能存在于 `mcpbridge` 和 `cmd/rin-mcp`。
- Rin Core 和 `host` 包不能导入 MCP SDK。

### 身份与权限

建议 Scope：

- `actor.read`
- `actor.converse`
- `actor.direct`
- `actor.execute`
- `operation.cancel`
- `host.admin`

首次连接使用本地一次性配对码或显式配置文件授权。授权记录绑定 Client、允许的
Host/World/Actor、Scope 和过期时间。高风险 Offer 可以额外要求游戏内确认；
确认结果必须绑定 Operation ID 和当前 Epoch。

审计记录只保存不透明 ID、Tool、Scope、状态、时间和延迟。默认不保存完整对话、
Prompt、API Key、Bearer Token 或游戏存档 Payload。

## 包和二进制边界

建议新增：

```text
controlplane/       领域对象、状态机、队列端口和权限规则
controlplane/store/ 持久化实现与恢复
mcpbridge/          MCP Tool 与 Control Plane Client 的转换
cmd/rin-mcp/        可选 MCP Server 二进制
sdk/*/control/      各语言 Host Control Client
```

`runtime`、`host` 和 `hostkit` 保持现有职责。Control Plane 可以复用 Host Contract
中的 Epoch、Offer、Invocation、Run 和 Outcome，但不能复制或弱化它们的验证规则。

## 分阶段实施

### R0：契约、威胁模型和 Fixture

交付：

- Control Plane 领域对象、限制常量和状态机设计；
- JSON Schema/OpenAPI 草案与协议版本协商规则；
- Principal、Scope、Lease、幂等和审计威胁模型；
- 一个内存 Fake Host 和跨语言 JSON Fixture。

测试：

- ID、大小、数量、时间和状态转换边界；
- 重复 JSON Property、未知字段、错误 Epoch 和过期 Lease；
- 同一 `request_id` 重试得到同一 Operation；
- Fuzz 状态机和 Schema。

### R1：只读 Control Plane

交付：

- Host 注册、租约、心跳和恢复；
- World/Actor/Snapshot/Offer 发布；
- 持久 Read Model 和失联状态；
- 列表与查询 API。

测试：

- Host 重启、Lease 超时、Actor 消失和 Snapshot 单调序号；
- Principal 隔离、脱敏和分页；
- 并发发布与查询 Race Test。

### R2：只读 MCP

交付：

- `cmd/rin-mcp`；
- STDIO Transport；
- `list_worlds`、`list_actors`、`get_actor_state`、`list_actor_offers`；
- Codex 与 Claude Code 配置样例和协议兼容测试。

验收：

- MCP Client 能发现 Actor，但无法产生任何世界修改；
- Client 断开不会影响 Rin 或游戏 Host；
- 无 Host 在线时返回明确状态，不伪造空世界成功。

### R3：写入队列和 Operation

交付：

- `send_actor_message`、`send_actor_directive`、`execute_actor_offer`；
- Host 长轮询、ACK、进度、终态 Outcome 和取消；
- 幂等 Request、容量上限、Backpressure 和 Outbox；
- Scope、配对、撤销和高风险确认。

测试：

- 响应丢失、重复投递、Rin 重启、Host 重启和迟到 Outcome；
- Queue 满、Outbox 满、取消竞态和 `outcome-unknown`；
- Offer 过期、Descriptor 改变、Epoch 改变和 Scope 撤销。

### R4：首个真实 Host 垂直切片

使用一个真实服务端游戏 Adapter 验证：

- 游戏内命令和 MCP 调用同一 Control Service；
- 读取、对话、Directive、精确 Offer 和长任务结果完整走通；
- 所有世界修改在游戏主线程完成；
- Operation、世界存档和 Rin Report 重启后可对账。

该阶段通过前，不新增大量 MCP Tool，也不宣称通用写控制已经稳定。

### R5：跨语言 Host Control SDK

按真实需求依次提供 Java、C#、Python、JavaScript 和 C/Lua 的最小 Client：

- 注册与续租；
- 发布 Snapshot/Offer；
- 领取、ACK 和回报 Control Request；
- 本地持久 Pending Request 和 Outcome Outbox；
- Conformance Fixture 与版本兼容测试。

SDK 只封装 Wire 和恢复语义，不提供引擎线程调度、对象解析或权限绕过。

### R6：事件订阅和多 Actor

- 有界 Operation Event Cursor 或 MCP Resource/Notification；
- Actor 间共享目标、角色分工和冲突仲裁；
- 每个 Actor 独立知识、权限和可见状态；
- Host 提供的资源锁、目标占用和公平调度；
- 批量操作仍保留独立 Operation ID 与 Outcome。

### R7：加固与 Preview 发布

- macOS、Windows、Linux 的 STDIO 与回环 HTTP 验收；
- Codex、Claude Code 和至少一个其他 MCP Client 的人工互操作；
- 24 小时重连、队列压力、故障注入和存储损坏测试；
- 日志脱敏、Token 轮换、依赖许可证和 SBOM；
- OpenAPI、SDK Inventory、中英文文档和示例无漂移。

## 测试矩阵

| 类别 | 必须覆盖 |
| --- | --- |
| 契约 | Schema、版本、未知字段、大小限制、Fuzz |
| 权限 | Client、Principal、World、Actor、Scope 和确认隔离 |
| 一致性 | 游戏内入口与 MCP 入口命中同一执行器和结果 |
| 时序 | Epoch、Deadline、Lease、TOCTOU、迟到结果 |
| 幂等 | 重试、重复 ACK、重复 Outcome、重启恢复 |
| 压力 | 队列、Outbox、分页、并发 Client、慢 Host |
| 安全 | 回环绑定、Origin、Token、日志脱敏、无任意参数 |
| 兼容 | MCP Client、操作系统、SDK 和 Host Fixture |

锁屏不影响单元测试、协议测试、Headless/GameTest、构建和静态检查。需要真人点击、
游戏内确认、视觉效果或长时间实际游玩的项目单独列入人工验收。

## 完成条件

首个 MCP 控制版本只有同时满足以下条件才可标记完成：

- 一个真实游戏证明 MCP 与游戏内入口使用同一执行服务；
- 只读和写入 Scope 可独立授予、撤销并被测试；
- 不存在任意 Capability、任意参数或任意对象引用入口；
- 重启和网络结果不明不会造成重复世界效果；
- 长任务可查询、可请求取消并能报告 `outcome-unknown`；
- MCP 不可用时游戏继续运行；
- 中英文协议、配置、运维和安全文档齐全；
- 剩余项目只包括明确列出的人工互操作与玩法体验验收。

## 提交约定

后续实施使用 Conventional Commits 类型和中文说明，例如：

```text
docs: 添加 MCP 控制面实施计划
feat: 增加 Host 租约与 Actor 快照
feat: 增加只读 MCP 工具
fix: 修复重复投递导致的操作重放
refactor: 统一游戏内与 MCP 控制入口
test: 增加 Host 重启恢复测试
```

每个阶段至少一个独立提交；协议、生成文件和测试应在同一阶段提交中保持一致。

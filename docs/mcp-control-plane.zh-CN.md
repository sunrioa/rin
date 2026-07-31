# MCP 与 Host Control 快速接入

[English](mcp-control-plane.md) | [简体中文](mcp-control-plane.zh-CN.md)

Rin 把外部控制拆成两个本地进程：

```text
Codex / Claude / MCP Client
        | STDIO
        v
     rin-mcp  --------\
     rin-mcp  --------+--> rin-control :7375 <--> 游戏 Host
     rin-mcp  --------/
```

- `rin-control` 是常驻 daemon，独占监听端口、Operation 状态目录、Bearer Token
  和固定可信 Principal。
- `rin-mcp` 是无状态 STDIO 薄代理，只把 MCP Tool 转换为 daemon Client API。
- 多个 MCP Client 可以同时各自启动 `rin-mcp`；关闭任一代理不会停止 daemon 或
  游戏 Host。
- 游戏 Host 通过同一 daemon 发布世界状态、领取请求并报告权威 Outcome。

当前使用官方 Go MCP SDK `v1.7.0-pre.3`，由 SDK 协商协议并优先使用
`2026-07-28`。该 SDK 与协议版本仍是 Preview；Rin CI 运行与自身能力匹配的官方
Conformance 场景。

## 构建

要求 Go 1.25 或更高版本：

```bash
go build -o bin/rin-control ./cmd/rin-control
go build -o bin/rin-mcp ./cmd/rin-mcp
```

创建本机令牌，配置一个游戏已经认可的 Principal，并启动 daemon：

```bash
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read"
export RIN_CONTROL_DATA_DIR="/absolute/path/to/rin-control-data"

./bin/rin-control
```

令牌至少 32 字节。`rin-control` 默认监听 `127.0.0.1:7375`，拒绝非回环地址。
正式配置应对数据目录使用绝对路径。

Host 发布 Actor 时，`owner_principal_id` 必须与配置的 Principal 一致，否则该
Actor 对普通 Client 不可见。`host.admin` 可以跨 Owner 读取，默认不应授予。

## MCP Client 配置

先保证 `rin-control` 已运行，再让 MCP Client 启动 `rin-mcp`：

```json
{
  "mcpServers": {
    "rin": {
      "command": "/absolute/path/to/rin/bin/rin-mcp",
      "env": {
        "RIN_CONTROL_URL": "http://127.0.0.1:7375",
        "RIN_CONTROL_TOKEN": "replace-with-the-same-random-secret"
      }
    }
  }
}
```

Principal 和 Scope 只由 `rin-control` 启动配置决定，不能由 MCP Tool 参数或代理
进程提升。代理的标准输出只承载 MCP Wire，诊断写入标准错误。

## Tool

`actor.read` 注册五个只读 Tool：

| Tool | 作用 |
| --- | --- |
| `list_worlds` | 列出当前 Principal 可见的世界 |
| `list_actors` | 列出一个世界中可见的 Actor |
| `get_actor_state` | 读取 Host 已脱敏发布的 Actor 状态 |
| `wait_actor_update` | 按观察序号与控制权修订号长轮询，最长等待 25 秒 |
| `list_actor_offers` | 读取在线 Host 当前发布的精确 Offer |

按 daemon Scope 还可注册：

| Tool | 最低 Scope | 作用 |
| --- | --- | --- |
| `send_actor_message` | `actor.converse` | 发送对白，不直接授权世界修改 |
| `send_actor_directive` | `actor.direct` | 提交 Actor 或 Host 可以拒绝的目标 |
| `speak_as_actor` | `actor.speak` | 由当前绑定的外部控制器提交角色对白 |
| `execute_actor_offer` | `actor.execute` | 选择一个完整、精确的当前 Offer |
| `get_operation` | 任一控制 Scope | 查询投递、运行和 Outcome |
| `wait_operation` | 任一控制 Scope | 按不透明 Cursor 等待 Operation 变化或可报告终态，最长 25 秒 |
| `cancel_operation` | `operation.cancel` | 请求取消，不表示回滚 |

例如需要对话和精确动作时：

```bash
export RIN_CONTROL_SCOPES="actor.read,actor.speak,actor.execute,operation.cancel"
```

修改 Scope 后重启 `rin-control`。外部角色循环通常先读取一次
`get_actor_state`，再以返回的 `observation_seq` 和 `decision_authority.revision`
调用 `wait_actor_update`；状态变化后可在同一个 `turn_id` 中调用
`speak_as_actor`，并按需选择一条 `execute_actor_offer`。两个 Operation 的安全视图
都会回显 `turn_id`，便于结果关联。

`execute_actor_offer` 不接受任意动作参数、坐标、物品 ID 或方法名。Operation 保存
Host 发布的完整 Offer 及其 Epoch、Observation 和 Authority Revision Binding；
游戏在权威线程执行前仍要复验 Offer、Deadline、权限和当前世界状态。

所有写 Tool 的直接返回只表示 Rin 已接收或排队，不能证明游戏已经执行。调用方必须
把返回的 `cursor` 原样交给 `wait_operation`，或继续调用 `get_operation`：

- `queued`、`delivered`、`accepted`、`running` 均不能汇报为完成；
- 只有 `execution_confirmed=true` 才能汇报 NPC 执行成功；该值仅在
  `status=succeeded` 且存在 Host 权威 `outcome` 时成立；
- `terminal=true` 表示已有可向用户报告的终态。`failed`、`rejected`、
  `cancelled`、`interrupted`、`stale` 和 `outcome-unknown` 都不能包装成成功；
- `stale` 且 `delivery_attempts=0` 表示 Host 从未领取该请求；
- `wait_operation.changed=false` 只表示等待期间没有新版本，不是任何执行证据；
- Host 状态是不透明数据，必须按显式 `subject` 与 `subject_id` 归属观察；属于其他
  主体的嵌套 Context 不能证明 Actor 行为。Actor 执行只能由 Operation Outcome 或
  Actor 自身的任务遥测证明。

`outcome-unknown` 是可报告的不确定终态，Host 后续仍可通过 Pending Journal 和
Outcome Outbox 对账；调用方不得在对账前猜测成功或失败。

## 角色控制权

Host 可以为 Actor 发布 `decision_authority`：

- `source=internal` 时，外部 Client 只能观察，不能代角色说话或选择 Offer；
- `source=external` 时，只有 `controller_principal_id` 精确匹配 daemon Principal
  的 Client 可以控制该 Actor，`host.admin` 也不能绕过这一绑定；
- `persona_mode=character-bound` 要求外部 Agent 扮演 Host 定义的角色；
- `persona_mode=agent-avatar` 允许外部 Agent 使用自己的性格与私有记忆来表现角色；
- 每次转交都会单调增加 `revision`。尚未被 Host 接受的旧修订 Operation 会失效，
  已接受的有界动作可以完成，避免半途破坏世界事务。

控制权只决定“谁做下一次语义决策”。导航、战斗和建造仍由 Host 的逐 Tick
控制器执行；Rin 不把模型输出转换成逐帧移动，也不复制任一控制器的私有记忆。

## Host 生命周期

游戏 Host 使用相同的 `RIN_CONTROL_URL` 和 Token：

1. `register` 获取 Lease。
2. `publish` 原子发布 World、Actor 与当前 Offer。
3. `poll` 长轮询领取消息、Directive、精确 Offer 和取消请求。
4. `ack` 表示接受或拒绝稳定 Operation ID。
5. 可选 `run` 上报单调进度。
6. `outcome` 上报唯一权威终态与最多 64 KiB 的严格 JSON `output`。
7. 周期性 `renew`，退出时 `unregister`。

Host 必须在游戏所属线程完成最终授权和世界修改。MCP、内部 AI 或游戏命令都应调用
同一个游戏执行服务，避免不同入口出现不同权限语义。

Control 契约由
[`api/control-openapi.json`](../api/control-openapi.json) 定义。Host 路由是：

| Method 与路径 | 作用 |
| --- | --- |
| `GET /control/v1/health` | 无鉴权存活检查 |
| `POST /control/v1/register` | 注册 Host 并取得 Lease |
| `POST /control/v1/renew` | 续租 |
| `POST /control/v1/unregister` | 主动离线 |
| `POST /control/v1/publish` | 发布一个 World Read Model |
| `POST /control/v1/poll` | 领取工作和取消通知 |
| `POST /control/v1/ack` | 接受或拒绝投递 |
| `POST /control/v1/run` | 上报运行进度 |
| `POST /control/v1/outcome` | 上报权威终态和结构化输出 |

Client 路径 `POST /control/v1/client/wait-operation` 提供相同的有界长轮询语义。

`/control/v1/client/*` 路由供 `rin-mcp` 的类型化 HTTP Client 使用。Client 请求体
不携带 Principal；daemon 始终注入启动时固定的 Principal，避免身份伪造。

## 持久化与恢复

`RIN_CONTROL_DATA_DIR` 中的 `operations.json` 使用 0600 权限、严格 JSON、
临时文件同步和原子替换。目录带跨平台进程锁，只能由一个 `rin-control` 写入。
状态文件不保存 Token、模型 Key、Prompt 或游戏存档。

恢复规则：

- 新入队请求、ACK、取消和 Outcome 立即持久化；
- 投递次数和 ActionRun 进度是检查点，在下一个耐久写入或正常关闭时合并；
- 进程在 ACK 前崩溃时，请求按相同 Operation ID 安全重投，投递计数可以重置；
- ACK 后尚无 Outcome 的请求恢复为 `outcome-unknown`，Host 应从自己的 Pending
  Journal/Outcome Outbox 对账；
- 绑定旧 Epoch、旧 Observation 或旧版无 Binding 的请求不会交给新时间线；
- 无 Host 的未完成请求在 TTL 后变为 `stale` 或 `outcome-unknown`，不会永久占满
  队列；
- Host Lease 和 Read Model 仍由 Host 所有，重连后必须重新注册和发布。

## 安全边界

- `rin-control` 和 `rin-mcp` 都只接受回环地址。
- 所有非健康检查请求必须携带同一个至少 32 字节的 Bearer Token。
- Token 不应放入仓库、Prompt、MCP 参数、游戏存档或日志。
- 相同 Principal 的相同 `request_id` 返回同一 Operation；不同 Payload 会冲突。
- Host 必须按 Operation ID 幂等处理重投。
- `output` 不得包含 API Key、完整 Prompt、未脱敏世界状态或引擎对象。
- 当前没有远程 MCP、动态配对、多 Principal 或同时多写控制器；这些都列在
  [路线图](../ROADMAP.md)，不是隐含支持能力。

`rin-mcp -conformance-addr` 只用于本地官方 Conformance 测试，并要求 daemon
Principal 只有 `actor.read`。它不是生产 Streamable HTTP 部署入口。

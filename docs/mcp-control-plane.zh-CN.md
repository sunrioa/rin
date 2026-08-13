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
go build -o bin/rin ./cmd/rin
go build -o bin/rin-control ./cmd/rin-control
go build -o bin/rin-mcp ./cmd/rin-mcp
```

创建本机令牌，配置一个游戏已经认可的 Principal，并启动 daemon：

```bash
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read,skill.read"
export RIN_CONTROL_DATA_DIR="/absolute/path/to/rin-control-data"

./bin/rin-control
```

令牌至少 32 字节。`rin-control` 默认监听 `127.0.0.1:7375`，拒绝非回环地址。
正式配置应对数据目录使用绝对路径。

Host 发布 Actor 时，`owner_principal_id` 必须与配置的 Principal 一致，否则该
Actor 对普通 Client 不可见。`host.admin` 可以跨 Owner 读取，默认不应授予。

## 一键配置 MCP Client

`rin` 可以检测 Codex、Claude Code 与 OpenClaw CLI，并通过各自的官方 MCP 命令
完成用户级注册。首次运行会列出已检测到的 Agent；输入编号、名称或直接回车选择
全部：

命令格式以 [Codex MCP](https://developers.openai.com/codex/mcp)、
[Claude Code MCP](https://code.claude.com/docs/en/mcp) 与
[OpenClaw MCP](https://docs.openclaw.ai/cli/mcp) 官方文档为准。

```bash
export RIN_CONTROL_TOKEN="replace-with-the-same-random-secret"
./bin/rin mcp install
```

非交互环境可以显式选择或接受所有已检测 Agent：

```bash
./bin/rin mcp install -agents codex,claude,openclaw
./bin/rin mcp install -yes
```

安装器执行以下操作：

1. 把同目录的 `rin-mcp` 原子安装到操作系统用户配置目录下的稳定路径；
2. 把回环 Control URL 和 Token 写入同目录的私密 `mcp-client.json`，Unix 权限为
   `0600`；
3. 通过 Agent 官方 CLI 注册 `rin-mcp --config <private-file>`。Agent 配置中不
   包含 Token；
4. 写入安装清单，记录由 Rin 接管的 Agent；明确失败且复查确认未写入时回滚记录。

使用 `--config` 时，该私密文件中的 Token 和 URL 不受 Agent 进程偶然继承的环境
环境变量影响；只有显式 `--control-url` 参数可以临时覆盖文件中的 URL。未使用
配置文件时，原有 `RIN_CONTROL_URL` 与 `RIN_CONTROL_TOKEN` 行为保持不变。

同名 `rin` MCP Server 若不是安装器创建的，默认拒绝覆盖。确认需要接管时使用
`-force`；修复已托管但被手工改坏的注册使用 `-repair`。安装完成后应重启或重载
对应 Agent Client。

检查所有 Agent、托管二进制和私密配置：

```bash
./bin/rin mcp status
```

状态输出不会显示 Token。Codex、Claude Code 或 OpenClaw 没有安装时只会标记为
未检测到，不影响其他 Client。

### 一键更新

从新版 Rin 发行目录运行：

```bash
./bin/rin mcp update
```

命令自动使用当前 `rin` 同目录的新版 `rin-mcp`，原子替换稳定托管路径，不改写
Agent 注册或私密连接配置；SHA-256 相同则不重复替换。也可明确指定已验证的二进制：

```bash
rin mcp update -server /absolute/path/to/new/rin-mcp
```

Windows 会锁定正在运行的可执行文件，更新前需要退出使用 Rin MCP 的 Agent；
macOS/Linux 也建议更新后重启 Agent，使已有 STDIO 会话加载新版本。

当前仓库没有承诺带 SHA-256 清单的自动 Binary Release Pipeline，因此该命令
不会静默从网络下载未验证程序。正式发布经过校验的二进制后，联网更新可保持相同
命令接口扩展。

### 卸载

默认移除清单内全部 Agent 注册，但保留托管文件以便重新安装；也可以只移除指定
Agent：

```bash
rin mcp uninstall
rin mcp uninstall -agents codex,claude
```

完全移除托管二进制、安装清单和私密连接配置：

```bash
rin mcp uninstall -purge
```

卸载器只删除自己记录的注册和固定托管路径，不删除同名未托管配置，也拒绝清理
符号链接或其他非普通文件。

## 手工配置

无法使用受支持 Agent CLI 时，仍可手工让 MCP Client 启动 `rin-mcp`。既可以沿用
环境变量，也可以使用安装器创建的中央配置文件：

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

中央配置形式的等价命令是：

```bash
/absolute/path/to/rin-mcp --config /absolute/path/to/mcp-client.json
```

Principal 和 Scope 只由 `rin-control` 启动配置决定，不能由 MCP Tool 参数或代理
进程提升。代理的标准输出只承载 MCP Wire，诊断写入标准错误。

`rin-control` 同时持有共享 Skill Catalog。它从数据目录下的
`skills/installed` 和 `skills/learned` 读取标准 `SKILL.md`；内置 Agent 与所有 MCP
代理看到同一份目录。Skill 只是攻略文字，不能授予 Capability 或绕过 Policy。

## Tool

`actor.read` 注册以下只读 Tool：

| Tool | 作用 |
| --- | --- |
| `list_worlds` | 列出当前 Principal 可见的世界 |
| `list_actors` | 列出一个世界中可见的 Actor |
| `get_actor_state` | 读取 Host 已脱敏发布的 Actor 摘要、控制权和急停状态 |
| `wait_actor_update` | 按观察、控制权和急停游标长轮询，最长等待 25 秒 |
| `observe_actor` | 读取 Host 发布的完整 V2 Observation 与不透明对象引用 |
| `list_actor_capabilities` | 列出 Actor 当前可发现的强类型能力摘要 |
| `describe_actor_capability` | 读取指定能力的输入、输出和效果 Schema |

所有已授权的 MCP Client 还可以查询 `get_operation` 和 `wait_operation`。其余 Tool
按 daemon Scope 注册：

| Tool | 最低 Scope | 作用 |
| --- | --- | --- |
| `acquire_actor_control` | `actor.control` | 获取唯一、绑定 Epoch 的语义控制租约 |
| `renew_actor_control` | `actor.control` | 续租同一个控制器 |
| `release_actor_control` | `actor.control` | 释放控制器并隔离未完成工作 |
| `get_actor_control` | `actor.control` | 读取当前有效控制器 |
| `confirm_action` | `actor.control` | 确认一次高风险策略挑战并使用最新 Host 快照复验 |
| `set_actor_emergency_stop` | `actor.control` | 由角色所有者或 Host 管理员设置、解除急停 |
| `submit_actor_action` | `actor.execute` | 提交一个严格符合 Host Capability Schema 的动作请求 |
| `cancel_operation` | `operation.cancel` | 请求取消，不表示回滚 |
| `list_skills` | `skill.read` | 按 Adapter 与可用 Capability 列出 Skill 摘要 |
| `get_skill` | `skill.read` | 读取指定版本的完整 Skill 指导 |
| `save_experience_as_skill` | `skill.write` | 把已核实的过程经验保存为 learned `SKILL.md` |
| `reload_skills` | `skill.write` | 从磁盘重新加载已安装和已学习 Skill |

例如允许外部 Agent 观察、控制并执行 Host 已注册能力：

```bash
export RIN_CONTROL_SCOPES="actor.read,actor.control,actor.execute,operation.cancel,skill.read"
```

需要让外部 Agent 写入经验时再增加 `skill.write`。该 Scope 只允许修改 learned
Skill，不会扩大游戏动作权限；`save_experience_as_skill` 的说明要求调用方只从权威
Outcome 总结成功经验，真正的自动沉淀和复核流程在后续阶段实现。

修改 Scope 后重启 `rin-control`。需要确认高风险效果时，Policy 文件按 Host Clock
分别配置 Challenge 有效期：

```json
{
  "confirmation_ttl": {
    "event": 16,
    "step": 600,
    "realtime": 30000
  }
}
```

值为 `0` 或省略表示该 Clock 禁止确认；策略会返回
`policy.confirmation_clock_disabled`，不会产生内部错误。Challenge 有效期始终不超过
对应 BoundAction 的 `valid_until`。

标准循环为：

1. 用 `get_actor_state`、`observe_actor` 读取当前 Epoch 与观察序号。
2. 用 `list_actor_capabilities`、`describe_actor_capability` 发现强类型能力。
3. 用 `acquire_actor_control` 获取短期独占控制租约，并在持续控制时定期续租。
4. 用精确的能力版本、Digest、观察游标和 HostRef 调用 `submit_actor_action`。
5. 若策略要求确认，调用 `confirm_action`；随后用 `wait_operation` 等待权威结果。

MCP 不能提交 Principal、Scope、效果预览、风险等级或策略结论。参数只表达意图；Host
在权威线程解析不透明引用、规范化参数并绑定真实效果，Rin 再按效果策略批准、拒绝或
要求确认。对白、采集、导航和建造在协议层没有特权，均由具体游戏以 Capability 注册。
复合任务只有在父操作被 Host 接受后，才能创建带 `parent_operation_id` 的可审计子
Operation。父能力必须发布为 `kind=macro` 且 `produces_child_operations=true`，父子
Action 还必须携带相同的非空 `task_id`；计划不能绕过相同的绑定、策略和最终授权。

所有写 Tool 的直接返回只表示 Rin 已接收或排队，不能证明游戏已经执行。调用方必须
把返回的 `cursor` 原样交给 `wait_operation`，或继续调用 `get_operation`：

- `queued`、`delivered`、`accepted`、`running` 均不能汇报为完成；
- 只有 `execution_confirmed=true` 才能汇报 NPC 执行成功；该值仅在
  `status=succeeded` 且存在 Host 权威 `outcome` 时成立；
- `terminal=true` 表示状态已经稳定，不会再收到不同的权威结果。`failed`、
  `rejected`、`cancelled`、`interrupted` 和 `stale` 都不能包装成成功；
- `reconciliation_pending=true` 表示当前为 `outcome-unknown`，Host 仍可能补交
  权威 Outcome；调用方应继续使用 `wait_operation`，不能把它当作最终成功或失败；
- `stale` 且 `delivery_attempts=0` 表示 Host 从未领取该请求；
- `wait_operation.changed=false` 只表示等待期间没有新版本，不是任何执行证据；
- Host 状态是不透明数据，必须按显式 `subject` 与 `subject_id` 归属观察；属于其他
  主体的嵌套 Context 不能证明 Actor 行为。Actor 执行只能由 Operation Outcome 或
  Actor 自身的任务遥测证明。

无权威 Outcome 的 `outcome-unknown` 会在有限保留期内等待 Host 通过 Pending
Journal 和 Outcome Outbox 对账，之后才能被清理，避免孤儿请求永久占满队列。
Host 主动报告且带 Outcome 的 `outcome-unknown` 则是稳定的不确定结果。

## 角色控制权

Host 可以为 Actor 发布 `decision_authority`：

- `source=internal` 时，普通外部 Client 只能观察；只有 Host 管理员能取得内部控制租约；
- `source=external` 时，只有 `controller_principal_id` 精确匹配 daemon Principal
  的 Client 可以取得普通控制租约；
- `persona_mode=character-bound` 要求外部 Agent 扮演 Host 定义的角色；
- `persona_mode=agent-avatar` 允许外部 Agent 使用自己的性格与私有记忆来表现角色；
- 每次转交都会单调增加 `revision`。尚未被 Host 接受的上一修订 Operation 会失效，
  已接受的有界动作可以完成，避免半途破坏世界事务。

控制权只决定“谁做下一次语义决策”。导航、战斗和建造仍由 Host 的逐 Tick
控制器执行；Rin 不把模型输出转换成逐帧移动，也不复制任一控制器的私有记忆。

## Host 生命周期

游戏 Host 使用相同的 `RIN_CONTROL_URL` 和 Token：

1. `register` 获取 Lease。
2. `publish` 原子发布 World、Actor、完整 Observation 与 Capability Snapshot。
3. `poll` 长轮询领取动作绑定/快照请求、已授权 Operation 和取消请求。
4. `gateway-result` 返回 Host 生成的 BoundAction 或最新权威快照。
5. Host 在权威线程再次校验 BoundAction 后用 `ack` 接受或拒绝稳定 Operation ID。
6. 可选 `run` 上报单调进度，`outcome` 上报唯一权威终态与严格 JSON `output`。
7. 周期性 `renew`，退出时 `unregister`。

Host 必须在游戏所属线程完成最终授权和世界修改。MCP、内部 AI 或游戏命令都应调用
同一个游戏执行服务，避免不同入口出现不同权限语义。

Control 契约由
[`api/control-openapi.json`](../api/control-openapi.json) 定义。Host 路由是：

| Method 与路径 | 作用 |
| --- | --- |
| `GET /control/v2/health` | 无鉴权存活检查 |
| `POST /control/v2/host/register` | 注册 Host 并取得 Lease |
| `POST /control/v2/host/renew` | 续租 |
| `POST /control/v2/host/unregister` | 主动离线 |
| `POST /control/v2/host/publish` | 发布一个 World Read Model |
| `POST /control/v2/host/poll` | 领取网关请求、Operation 和取消通知 |
| `POST /control/v2/host/gateway-result` | 返回动作绑定或权威快照 |
| `POST /control/v2/host/ack` | 接受或拒绝投递 |
| `POST /control/v2/host/run` | 上报运行进度 |
| `POST /control/v2/host/outcome` | 上报权威终态和结构化输出 |

`/control/v2/*` Client 路由供 `rin-mcp` 和语言 SDK 使用，覆盖发现、控制租约、动作、
Operation 与急停。Client 请求体不携带 Principal；daemon 始终注入启动时固定的
Principal，避免身份伪造。Host 只使用 `/control/v2/host/*`。

错误响应始终包含供人阅读的 `error`，并可包含稳定的机器可读 `code`。当前服务码为
`invalid`、`forbidden`、`not_found`、`lease_expired`、`unavailable`、
`lease_conflict`、`stale`、`not_accepted`、`conflict`、`capacity` 和 `internal`。Client 应在
`code` 存在时按它分支，并把 HTTP 状态码作为兼容回退。

## 持久化与恢复

`RIN_CONTROL_DATA_DIR` 中的 `operations.json` 使用 0600 权限、严格 JSON、
临时文件同步和原子替换。目录带跨平台进程锁，只能由一个 `rin-control` 写入。
状态文件使用 `rin.control.operations/v5` 的 Action-only Schema，
也不保存 Token、模型 Key、Prompt 或游戏存档。

恢复规则：

- 新入队请求、ACK、取消和 Outcome 立即持久化；
- 投递次数和 ActionRun 进度是检查点，在下一个耐久写入或正常关闭时合并；
- 进程在 ACK 前崩溃时，原绑定不可安全重用，请求恢复为 `stale`，模型必须基于最新
  Observation 重新提交；
- ACK 后尚无 Run 或 Outcome 的请求按相同 Operation ID 重投，由 Host 从持久
  Pending Journal 恢复；
- 已报告执行但尚无 Outcome 的请求恢复为 `outcome-unknown`，Host 应从自己的
  Outcome Outbox 对账；
- 已持久化的 `stale` 或未解决 `outcome-unknown` 在重启后不会复活为 queued 或
  accepted 请求；
- 绑定过期 Epoch 或 Observation 的请求不会交给新时间线；
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

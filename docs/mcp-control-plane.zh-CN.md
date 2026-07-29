# MCP 2026-07-28 快速接入

[English](mcp-control-plane.md) | [简体中文](mcp-control-plane.zh-CN.md)

`rin-mcp` 是 Rin 的可选外部控制 Gateway。当前版本只实现只读阶段：

- 使用官方 Go MCP SDK `v1.7.0-pre.3`；
- 仅接受 MCP `2026-07-28`，使用 `server/discover`，不降级到旧协议；
- MCP Client 使用 STDIO；
- 游戏 Host 使用带 Bearer Token 的回环 HTTP 发布状态；
- MCP 与 Host 共享一个进程内 Control Plane；
- 当前没有修改世界的 Tool。

写操作、Operation、取消和 Streamable HTTP MCP 属于后续阶段，详见
[实施计划](mcp-control-plane-plan.zh-CN.md)。

## 构建

要求 Go 1.25 或更高版本：

```bash
go build -o bin/rin-mcp ./cmd/rin-mcp
```

创建独立令牌并选择游戏内已授权的 Principal：

```bash
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read"
```

令牌至少包含 32 字节。不要把令牌放入仓库、MCP 参数、Prompt、游戏存档或日志。
Host 发布 Actor 时，`owner_principal_id` 必须与该 Principal 一致；否则该 Actor
不会对 MCP Client 可见。

## MCP Client 配置

把二进制作为本地 STDIO Server 配置。不同 Client 的配置文件位置不同，但 Server
条目遵循同一结构：

```json
{
  "mcpServers": {
    "rin": {
      "command": "/absolute/path/to/rin/bin/rin-mcp",
      "env": {
        "RIN_CONTROL_TOKEN": "replace-with-a-random-secret",
        "RIN_CONTROL_PRINCIPAL": "player.one",
        "RIN_CONTROL_SCOPES": "actor.read"
      }
    }
  }
}
```

进程的标准输出只承载 MCP Wire。诊断信息写入标准错误。

## 已实现 Tool

| Tool | 作用 |
| --- | --- |
| `list_worlds` | 列出当前 Principal 可见的世界 |
| `list_actors` | 列出指定世界中可见的 Actor |
| `get_actor_state` | 读取 Host 已脱敏发布的 Actor 状态 |
| `list_actor_offers` | 读取在线 Host 当前提供的精确 Action Offer |

四个 Tool 都标记为只读和幂等。离线 Host 的最后状态可继续读取，但 Offer 不可用。
`host.admin` 能读取所有主体，只应由明确受信任的本机配置授予。

## Host 发布端点

`rin-mcp` 默认只监听 `127.0.0.1:7375`，且不能配置为非回环地址。所有
`/control/v1/*` 请求都必须携带：

```http
Authorization: Bearer <RIN_CONTROL_TOKEN>
Content-Type: application/json
```

当前端点：

| Method 与路径 | 作用 |
| --- | --- |
| `GET /health` | 无鉴权存活检查 |
| `POST /control/v1/register` | 注册 Host 并取得 Lease |
| `POST /control/v1/renew` | 续租 |
| `POST /control/v1/publish` | 原子发布一个 World Read Model |
| `POST /control/v1/unregister` | 主动离线并保留最后 Read Model |

请求体默认不超过 1 MiB，拒绝未知字段和重复 JSON Property。Host 必须按
`register -> publish -> renew -> unregister` 生命周期调用。世界修改仍由游戏的
权威线程负责，Control Plane 不保存或解析引擎对象。

## 权限边界

- `actor.read` 只能读取 `owner_principal_id` 与自身相同的 Actor。
- `host.admin` 可读取全部 Actor，应默认关闭。
- Scope 来自启动配置，模型输出和 MCP Tool 参数不能提升权限。
- Offer 只是 Host 已绑定的候选动作；当前阶段没有执行 Offer 的 Tool。
- MCP Client 退出只关闭 Gateway 进程，不改变游戏世界。

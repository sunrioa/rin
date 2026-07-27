# 部署与监控

[简体中文](operations.zh-CN.md) | [English](operations.md)

Rin 将 Liveness、Readiness、Diagnostics 和 Metrics 分开暴露。这些接口只包含
Counter 与状态分类；绝不包含 Session ID、Actor ID、Event 文本、模型
Prompt/Response、凭据或文件系统路径。

## Probe 与鉴权

| Endpoint | 鉴权 | 含义 |
| --- | --- | --- |
| `GET /health` | 无 | 廉价的进程 Liveness 与兼容身份；不访问 Store 或 Provider |
| `GET /ready` | 无 | Session Store 可执行 List，且每个已配置 Job Manager 仍在运行 |
| `GET /v2/diagnostics` | 设置 `RIN_TOKEN` 时使用 Bearer Token | Runtime、Queue、Retained Job、Checkpoint、Uncertainty 与 Provider Breaker 的有界 JSON Snapshot |
| `GET /metrics` | 设置 `RIN_TOKEN` 时使用 Bearer Token | 无依赖的 Prometheus 文本；Metric Name 固定且没有高基数 Label |

Liveness Probe 使用 `/health`，Readiness Probe 使用 `/ready`。Readiness 失败返回
`503 not_ready`；不得用 `/health` 判断是否应把游戏流量路由到该实例。

配置 `RIN_TOKEN` 后，鉴权 Route 只接受一个
`Authorization: Bearer <token>` Header（Scheme 大小写不敏感）；无前缀凭据、
多余字段、Credential 内空白与重复 Header 都会被拒绝。未配置 Token 时，包括
Probe 在内的每个请求都必须使用 Loopback `Host`；Browser 请求若带 `Origin`，
必须与 Loopback Host 同源，且 `Sec-Fetch-Site` 只能是 `same-origin` 或
`none`。Native 本地游戏 Client 可继续工作，同时会拒绝 DNS Rebinding 与跨站
Browser 请求。

Linux/macOS：

```bash
curl --fail http://127.0.0.1:7374/health
curl --fail http://127.0.0.1:7374/ready
curl --fail -H "Authorization: Bearer $RIN_TOKEN" \
  http://127.0.0.1:7374/v2/diagnostics
```

Windows PowerShell：

```powershell
Invoke-RestMethod http://127.0.0.1:7374/health
Invoke-RestMethod http://127.0.0.1:7374/ready
$headers = @{ Authorization = "Bearer $env:RIN_TOKEN" }
Invoke-RestMethod -Headers $headers http://127.0.0.1:7374/v2/diagnostics
```

Windows 本地游戏接入可把 `rin.exe` 与可写的 `rin-data` 目录放在一起，并使用
仓库内启动器：

```powershell
powershell -ExecutionPolicy Bypass -File tools/start-rin.ps1 `
  -Rin .\rin.exe -DataDirectory .\rin-data
```

启动器使用 Literal Path，只创建指定数据目录，默认仅绑定 Loopback，并透传
Sidecar Exit Code。启动游戏前请检查 `/ready`。

## 远程部署

受支持的远程路径必须由可信 Reverse Proxy 终止 TLS，并始终配置非空
`RIN_TOKEN`。优先让 Proxy 与 Rin 运行在同一台主机：Rin 继续使用默认 Loopback
监听，不需要开启远程监听。

```bash
export RIN_TOKEN="$(openssl rand -hex 32)"
rin serve -addr 127.0.0.1:7374
```

使用 DNS 域名并由 Caddy 管理 HTTPS 的最小 Caddyfile：

```caddyfile
rin.example.com {
    @private path /metrics /v2/diagnostics
    respond @private 404
    reverse_proxy 127.0.0.1:7374
}
```

客户端通过 HTTPS 发送 `Authorization: Bearer <token>`。不得把 Token 写入 Proxy
日志，也不得公开 `/metrics` 或 `/v2/diagnostics`；应在本机采集它们。生产 Proxy
控制项见 Caddy 官方
[Reverse Proxy 文档](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)。

若 Proxy 与 Rin 必须分处不同主机，只能在私网监听明文端口，并用防火墙限制为
仅 Proxy 可访问。此时 Rin 要求同时作出三项声明：

```bash
export RIN_TOKEN="$(openssl rand -hex 32)"
export RIN_TLS_PROXY=true
rin serve -addr 10.0.0.12:7374 -allow-remote
```

`-tls-proxy` 等价于 `RIN_TLS_PROXY=true`。它只声明已有可信 Proxy 负责 TLS，
不会为 Rin 启用 TLS，也不能让公网明文监听变安全。非 Loopback 监听若缺少
`-allow-remote`、Token 或该声明，会在打开数据目录前直接失败。

容量、并发、Timeout 与 Boolean 环境变量一旦显式设置为非法值，Rin 会立即失败，
不会把拼写错误静默替换成默认值；命令行显式 Limit 也遵循同一规则。Runtime、
Proposal Job 与 Generation 的上下限全部在打开数据目录或执行恢复维护前完成校验，
因此被拒绝的配置不会创建或维护 Store 文件。

随附 Sidecar 会在启动后立即运行一次 checkpoint-independent Event Log Scrub，
之后默认每 15 分钟运行一次。每个 Pass 最多校验 4,096 个事件，Deadline 为
30 秒。可通过 `RIN_SCRUB_INTERVAL`、`RIN_SCRUB_MAX_EVENTS`、
`RIN_SCRUB_TIMEOUT` 或对应 `-scrub-*` Flag 配置。Timeout 会保留已经验证的
Cursor，下个 Pass 从该位置继续；显式一次性全量审计仍使用
`Engine.VerifyAll()`。

## 监控内容

JSON Diagnostics Snapshot 包含：

- 已知、已加载和当前不可读 Session 数，以及有界错误码分组；
- 尚未解决的持久 Mutation Barrier；
- Active/Pending Checkpoint、Checkpoint Failure 与 Quota Skip；
- Incremental Scrub 是否 Active、Cursor Revision/Target、Failure 与完成
  Cycle 数；
- Runtime Closed 状态与 Active Engine Operation 数；
- Proposal/Generation Queue Depth/Capacity、Retained/Max-retained Job 和状态计数；
- Generation Cache 大小、Retained Payload 当前字节数与配置上限，以及 Provider
  Circuit Breaker 状态；
- HTTP Request、In-flight、4xx/5xx 以及累计耗时。

Prometheus 输出使用固定名称，包括 `rin_http_requests_total`、
`rin_sessions_unreadable_known`、`rin_uncertainty_barriers`、
`rin_checkpoint_failures_total`、`rin_scrub_completed_cycles_total`、
`rin_scrub_failures_total`、`rin_scrub_active`、`rin_proposal_queue_depth`，
以及配置 Generation 后的 `rin_provider_circuit_not_closed`。

建议告警：

- Readiness 连续超过一个 Probe Window 失败；
- Known Unreadable Session、Uncertainty Barrier、Checkpoint Failure 或
  Scrub Failure 增长；
- Queue 长时间接近 Capacity，或 Retained Job 接近上限；
- Provider Circuit Breaker 长时间未关闭；
- 5xx Response 增长。

单纯 Queue Full 不会使进程 Unready：在高负载时摘除健康实例会放大饱和。
Queue Metric 与 `429` Response 才是 Overload Signal。

## Request Correlation 与停机

Client 可发送 `Rin-Request-ID`，值为 1–96 个 ASCII 字母、数字、`.`、`_` 或
`-`。Rin 会回显合法值，否则生成新值。结构化 Request Log 只包含该 ID、
Method、匹配后的 Route Template、Status 和 Duration；不记录 Raw Path、
Query、Header 或 Body。

停机时，应在 `/ready` 失败后停止路由新流量，让 HTTP Server Drain，关闭
Job Manager，调用 `Engine.Close(ctx)`，最后才能关闭由调用方拥有的 Store。
`Engine.Close` 会拒绝新操作，并等待在途 Request、Transfer Writer 与
Checkpoint Worker。随附 CLI 会先取消并 Join 后台 Scrub，再在 SIGINT/SIGTERM
（或对应 Windows Console/Service Signal）后按此顺序执行有界 Graceful
Shutdown。不得绕过
[Session 生命周期](session-lifecycle.zh-CN.md)的备份规则复制或修改在线
Data Directory。

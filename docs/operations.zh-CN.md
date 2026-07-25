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
| `GET /v1/diagnostics` | 设置 `RIN_TOKEN` 时使用 Bearer Token | Runtime、Queue、Retained Job、Checkpoint、Uncertainty 与 Provider Breaker 的有界 JSON Snapshot |
| `GET /metrics` | 设置 `RIN_TOKEN` 时使用 Bearer Token | 无依赖的 Prometheus 文本；Metric Name 固定且没有高基数 Label |

Liveness Probe 使用 `/health`，Readiness Probe 使用 `/ready`。Readiness 失败返回
`503 not_ready`；不得用 `/health` 判断是否应把游戏流量路由到该实例。

Linux/macOS：

```bash
curl --fail http://127.0.0.1:7374/health
curl --fail http://127.0.0.1:7374/ready
curl --fail -H "Authorization: Bearer $RIN_TOKEN" \
  http://127.0.0.1:7374/v1/diagnostics
```

Windows PowerShell：

```powershell
Invoke-RestMethod http://127.0.0.1:7374/health
Invoke-RestMethod http://127.0.0.1:7374/ready
$headers = @{ Authorization = "Bearer $env:RIN_TOKEN" }
Invoke-RestMethod -Headers $headers http://127.0.0.1:7374/v1/diagnostics
```

Windows 本地游戏接入可把 `rin.exe` 与可写的 `rin-data` 目录放在一起，并使用
仓库内启动器：

```powershell
powershell -ExecutionPolicy Bypass -File tools/start-rin.ps1 `
  -Rin .\rin.exe -DataDirectory .\rin-data
```

启动器使用 Literal Path，只创建指定数据目录，默认仅绑定 Loopback，并透传
Sidecar Exit Code。启动游戏前请检查 `/ready`。

Diagnostics 与 Metrics 应按其他鉴权 API 数据保护。Sidecar 默认只绑定 Loopback；
若 Reverse Proxy 对外暴露，必须使用 TLS，并避免把 `/metrics` 与
`/v1/diagnostics` 放到公网 Route。

## 监控内容

JSON Diagnostics Snapshot 包含：

- 已知、已加载和当前不可读 Session 数，以及有界错误码分组；
- 尚未解决的持久 Mutation Barrier；
- Active/Pending Checkpoint、Checkpoint Failure 与 Quota Skip；
- Proposal/Generation Queue Depth/Capacity、Retained/Max-retained Job 和状态计数；
- Generation Cache 大小与 Provider Circuit Breaker 状态；
- HTTP Request、In-flight、4xx/5xx 以及累计耗时。

Prometheus 输出使用固定名称，包括 `rin_http_requests_total`、
`rin_sessions_unreadable_known`、`rin_uncertainty_barriers`、
`rin_checkpoint_failures_total`、`rin_proposal_queue_depth`，以及配置
Generation 后的 `rin_provider_circuit_not_closed`。

建议告警：

- Readiness 连续超过一个 Probe Window 失败；
- Known Unreadable Session、Uncertainty Barrier 或 Checkpoint Failure 增长；
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

停机时，应在 `/ready` 失败后停止路由新流量，让 HTTP Server Drain，再关闭
Job Manager。随附 CLI 在 SIGINT/SIGTERM（或对应 Windows Console/Service
Signal）后执行有界 Graceful Shutdown。不得绕过
[Session 生命周期](session-lifecycle.zh-CN.md)的备份规则复制或修改在线
Data Directory。

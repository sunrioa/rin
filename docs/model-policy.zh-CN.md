# 模型策略

[English](model-policy.md) | [简体中文](model-policy.zh-CN.md)

模型接入是可选且有界的；没有供应商时，本地验证与确定性策略仍然可用。

## 启用

Rin 默认使用 `deterministic`，不会产生任何模型网络请求。在线模式需要显式配置：

```bash
export RIN_POLICY=model
export RIN_MODEL_BASE_URL="https://provider.example/v1"
export RIN_MODEL="your-model-id"
export RIN_MODEL_API_KEY="..."
rin serve
```

Rin 使用 OpenAI-compatible `POST <base-url>/chat/completions`。默认请求严格 `json_schema`；若供应商只支持 JSON Object：

```bash
export RIN_MODEL_RESPONSE_FORMAT=json_object
```

也可设为 `none`，但返回文本仍必须是单个、严格、无额外字段的 JSON Object。

## 环境变量

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `RIN_POLICY` | `deterministic` | `deterministic` 或 `model` |
| `RIN_MODEL_BASE_URL` | - | OpenAI-compatible `/v1` 基地址 |
| `RIN_MODEL` | - | 供应商模型 ID |
| `RIN_MODEL_API_KEY` | - | 只从进程环境读取的 Bearer Key |
| `RIN_MODEL_RESPONSE_FORMAT` | `json_schema` | `json_schema`、`json_object`、`none` |
| `RIN_MODEL_ATTEMPT_TIMEOUT` | `15s` | 单次 HTTP 尝试上限 |
| `RIN_MODEL_TOTAL_TIMEOUT` | `25s` | 包含重试与退避的总上限 |
| `RIN_MODEL_MAX_ATTEMPTS` | `2` | 最大尝试次数，上限 5 |
| `RIN_MODEL_INITIAL_BACKOFF` | `150ms` | 初始退避 |
| `RIN_MODEL_MAX_BACKOFF` | `2s` | 退避及 Retry-After 上限 |
| `RIN_MODEL_BREAKER_FAILURES` | `3` | 打开熔断器前的失败调用数 |
| `RIN_MODEL_BREAKER_OPEN` | `20s` | 熔断开放时间 |
| `RIN_MODEL_CACHE_ENTRIES` | `256` | 内存 Draft 缓存条数 |
| `RIN_MODEL_CACHE_TTL` | `10m` | 相同 head hash 缓存寿命 |
| `RIN_JOB_WORKERS` | `2` | 异步 Proposal worker 数 |
| `RIN_JOB_QUEUE_SIZE` | `64` | 等待队列大小 |
| `RIN_JOB_MAX_RETAINED` | `512` | 包含完成项的最大 Job 数 |
| `RIN_JOB_TTL` | `30m` | 完成 Job 的内存保留时间 |
| `RIN_GENERATION_WORKERS` | `2` | 结构化生成 worker 数 |
| `RIN_GENERATION_QUEUE_SIZE` | `64` | 生成等待队列大小 |
| `RIN_GENERATION_MAX_RETAINED` | `512` | 包含完成项的最大生成 Job 数 |
| `RIN_GENERATION_JOB_TTL` | `30m` | 完成生成 Job 的内存保留时间 |
| `RIN_GENERATION_CACHE_ENTRIES` | `256` | 语义生成缓存条数 |
| `RIN_GENERATION_CACHE_TTL` | `30m` | 语义生成缓存寿命 |
| `RIN_GENERATION_MAX_OUTPUT_BYTES` | `524288` | 单个结构化结果最大字节数 |

时长采用 Go duration，例如 `250ms`、`15s`、`2m`。

## 本地模型

Loopback 地址允许 HTTP 和空 Key：

```bash
export RIN_POLICY=model
export RIN_MODEL_BASE_URL="http://127.0.0.1:11434/v1"
export RIN_MODEL="local-model"
```

非 loopback HTTP 默认拒绝。只有受控测试网络才应显式设置 `RIN_MODEL_ALLOW_INSECURE=true`。

## 运行时行为

1. 游戏提交异步 Proposal Job。
2. 本地 Boundary Guard 先处理必须拒绝或重定向的情况。
3. Cache 按当前 Session head hash 查找不可变 Draft。
4. 未命中时构造最小、数据隔离的模型 Packet。
5. Provider 在总预算内调用、重试或熔断。
6. JSON Draft 经本地白名单验证。
7. 任一步失败时使用确定性 Policy，`policy_source=deterministic-fallback`。
8. Engine 再检查当前 revision/head hash；变化则 Job 为 `stale`。

模型只决定“建议执行哪个允许动作以及如何表达”，不能直接 commit，也不能改变世界状态。

结构化 Generation API 复用相同 Provider 与熔断预算，但它不使用确定性 Policy 回退。调用方必须自己准备离线文本，并在接受结果前执行领域 Schema 与 Canon 校验。

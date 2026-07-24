# 模型策略

[English](model-policy.md) | [简体中文](model-policy.zh-CN.md)

模型接入是可选且有界的；没有供应商时，本地验证与确定性策略仍然可用。

本文描述 Rin `0.6.0` Preview。Provider Response 是独立于游戏侧 HTTP Request
契约的另一条不可信边界。

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

## Provider 韧性契约

超时配置是“协作式预算”，不是运行时强制抢占。每个 `provider.Client` 实现都必须在输入
`ctx.Done()` 关闭后立即停止工作并尽快返回；内置 OpenAI-compatible Client 遵守该契约。
Go 无法安全终止任意阻塞实现，因此 Rin 不会把每次调用分离到可能永久泄漏的 goroutine 中。
不合规的第三方 Client 可能超过任一配置超时，并让 `Complete` 一直延迟到它实际返回。

对于遵守 Context 契约的 Client，`RIN_MODEL_ATTEMPT_TIMEOUT` 限制单次 HTTP 尝试。
若该截止时间到期时调用方与总预算仍然有效，
本次结果归类为“单次尝试超时”：即使客户端在截止时间之后返回成功，Rin 也会丢弃该结果，
并只在剩余总预算内重试。

在同一契约前提下，`RIN_MODEL_TOTAL_TIMEOUT` 覆盖一次逻辑 Provider 调用中的全部尝试、
`Retry-After` 与本地退避。
若调用方 Context 仍然有效而这项内部总预算到期，Rin 返回 `context deadline exceeded`，
丢弃迟到的成功或错误，并仅为熔断器记录一次逻辑调用失败。可重试尝试全部耗尽时同样只记录
一次逻辑调用失败，而不是按每次 HTTP 尝试分别累计。

调用方取消（包括调用方自己设置的 deadline）具有更高优先级：Rin 返回调用方的 Context
错误，不计为 Provider 失败；若取消发生在 half-open 探测中，还会释放探测名额，让后续调用
可以继续验证恢复情况。

只有单次尝试超时及明确归类为瞬态的错误才会重试，例如传输/响应读取失败、HTTP
408/429/5xx、成功响应格式损坏或内容为空，以及受上限约束的 `Retry-After`。本地请求或
Schema 错误、非瞬态 HTTP 4xx 与 `response_too_large` 均不重试。特别是响应字节上限不会
随尝试次数变化，重放超大响应只会消耗同一总预算，并不会放宽限制。

非重试结果使用独立的可用性语义。确认来自 HTTP/Provider 的响应会清空可用性失败序列，
也可以关闭 half-open 熔断；Schema 校验、请求编码或请求构造等本地预检错误属于中性结果：
它们不会清空 closed 状态的失败序列，也不能宣称 Provider 已恢复；half-open 中的预检错误
只释放探测名额。

连续 `RIN_MODEL_BREAKER_FAILURES` 次逻辑调用失败后，新调用会直接失败，不再访问 Provider。
经过 `RIN_MODEL_BREAKER_OPEN` 后只允许一个 half-open 探测，并发探测仍会快速失败。预算内
成功或非瞬态响应会结束可用性探测；可重试失败或内部总超时会让熔断器重新开放完整的等待
周期；调用方取消只释放探测名额。

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
5. Provider 在协作式总预算内调用、重试或熔断。
6. JSON Draft 只能包含 action/stance 与允许的审计 ID，不含玩家文本字段，并经
   本地未知字段与白名单验证。
7. Engine 丢弃所有 Policy 实现的兼容文本，只用选中的游戏编写动作描述与固定
   stance 模板重建玩家可见的 `summary`/`rationale`。
8. 前述任一步失败时使用确定性 Policy，`policy_source=deterministic-fallback`。
9. Engine 再检查当前 revision/head hash；变化则 Job 为 `stale`。

模型只决定建议哪个允许动作，以及哪些已提供的 Memory/Goal ID 参与了选择。私有 Goal、
Boundary、Memory、Belief、Trait、Intent 和近期上下文文本可以影响选择，但绝不能复制到
输出；严格 Schema 会拒绝 `summary` 与 `rationale` 字段。Rin 的运行时重建是最终信息流
门禁，不依赖私密字符串匹配。模型不能直接 commit，也不能改变世界状态。

`policy_source`、`recalled_memory_ids`、`goal_id`、运行时推导的 `boundary_id`
以及完整 `proposed_goal` 都是私有审计/集成字段；它们的存在或取值可能泄露角色
隐藏状态，玩家 UI 不得直接展示。

结构化 Generation API 复用相同 Provider 与熔断预算，但它不使用确定性 Policy 回退。调用方必须自己准备离线文本，并在接受结果前执行领域 Schema 与 Canon 校验。

Sidecar 会在 JSON 解码前严格拒绝游戏侧 HTTP Request 与成功 Provider JSON
Response 中的非法 UTF-8 和未配对 Unicode Surrogate。Rin 随后检查解码后的
Generation Content 是否为空、包含 NUL、字节超限以及是否为一个顶层 JSON
Object。非 2xx Provider Body 只用于有界错误分类，不会成为 Content 或 Session
State。

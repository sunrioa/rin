# 内部 Agent Runtime

[English](internal-agent-runtime.md) | [简体中文](internal-agent-runtime.zh-CN.md)

内部 Agent Runtime 是 `rin-control` 的可选组件。它使用人格、记忆、Skill 和模型
持续推进有预算的任务，但所有世界读取、控制租约、策略判断、Operation 和权威结果仍
经过同一个 Control Plane。未提供 Agent 配置时，`rin-control` 保持原有行为。

## 身份边界

| 身份 | 来源 | 权限 |
| --- | --- | --- |
| Control Client | `RIN_CONTROL_PRINCIPAL` 与 `RIN_CONTROL_SCOPES` | `/control/v2` |
| Agent Client | 配置中的 `client_principal` | 仅 `task.read`、`task.execute`、`task.cancel` |
| Internal Runtime | 进程内创建，不通过 HTTP 暴露 | 仅控制 `DecisionAuthority=internal` 的角色 |

`RIN_CONTROL_TOKEN` 与 `RIN_AGENT_TOKEN` 应使用不同值。任一 Token 都不能访问
另一组路由。Agent Client 不能配置 `host.admin`、`actor.*` 或游戏专属 Scope。

## 配置

创建只允许当前用户读取的 JSON 文件，例如 `/absolute/path/agent.json`：

```json
{
  "contract_version": "rin.agent.config/v1",
  "client_principal": {
    "id": "rin.agent-client",
    "granted_scopes": ["task.read", "task.execute", "task.cancel"]
  },
  "runtime_principal_id": "rin.internal",
  "model": {
    "provider": "openai-compatible",
    "base_url": "https://api.example.com/v1",
    "model": "example-model",
    "response_format": "json_schema",
    "authentication": "bearer-env",
    "max_context_characters": 64000,
    "max_output_tokens": 1500,
    "temperature": 0.2
  },
  "personas": [{
    "persona_id": "companion",
    "version": "v1",
    "identity": "A grounded companion.",
    "initiative_policy": {
      "enabled": true,
      "cooldown_millis": 5000,
      "max_consecutive_actions": 4
    }
  }],
  "persona_bindings": [{
    "persona_id": "companion",
    "version": "v1"
  }]
}
```

不填写 `actor_id` 的绑定是运行时动态 Actor 的显式默认人格。精确的
Actor+Controller 绑定和 Actor 绑定优先；只能配置一个默认绑定，且默认绑定不能选择 Controller。

```bash
chmod 600 /absolute/path/agent.json
export RIN_AGENT_CONFIG=/absolute/path/agent.json
export RIN_AGENT_TOKEN="$(openssl rand -hex 32)"
export RIN_AGENT_API_KEY="provider-key-from-secret-store"
./bin/rin-control
```

API Key 不是配置字段；在 JSON 中加入 `api_key` 会被拒绝。连接无认证的本地
OpenAI-compatible 服务时，把 `authentication` 设为 `none` 并确保
`RIN_AGENT_API_KEY` 未设置。启动只校验配置，不向模型服务发送探测请求。

## 状态与调用

- Task HTTP 契约以 [`api/agent-openapi.json`](../api/agent-openapi.json) 为准。
- 状态固定写入 `<RIN_CONTROL_DATA_DIR>/agent/tasks.json` 和 `memory.json`。
- Task Snapshot 使用 `rin.cognition.tasks/v2`。Preview 版本不读取 v1；升级前应结束或取消
  旧内部任务，不复制运行中的 Operation 状态。
- 状态文件使用私有权限、原子替换和单写者进程锁；配置不能改变状态路径。
- `scheduled=true` 只表示任务已进入后台队列，不表示模型已决策、游戏已执行或
  目标已经完成。
- `allowed_capabilities` 是可选的任务级 Capability ID 白名单，最多 128 项。非空时，
  Runtime 只向模型公开 Host 当前 Catalog 与该白名单的交集，并在恢复 Pending Action 时
  再次复验；空数组表示使用 Host 当前完整 Catalog。该字段只能收窄能力，不能创建 Host
  未发布的能力或绕过 Policy。
- 停止时先取消并等待 Agent worker，再释放 Task/Memory 锁，最后关闭 Control Plane。

## Macro 父子循环

内部 Runtime 与外部 MCP 使用同一套父子 Operation 契约。模型选择声明
`kind=macro` 且 `produces_child_operations=true` 的能力后，只有 Host 将父 Operation
推进到 `accepted` 或 `running`，Task 才记录 `macro_operation_id` 并进入下一次观察。
后续模型请求携带可信 `parent_operation_id`，所选 Atomic Child 仍逐项经过 Host Binding、
Policy、执行与权威 Outcome。

- queued、delivered、awaiting-confirmation、accepted 和 running 都不是完成证据；父 Operation 只有
  权威终态后才从 Task 清除。
- 运行父 Macro 时，模型只看到 Atomic Capability；Control Plane 仍支持嵌套 Macro，但当前
  内部 Runtime 不自动创建第二层父任务。使用任务级白名单时，必须同时列出父 Macro 和
  预期 Child；进入 Macro 阶段不会自动扩大任务权限。
- 有运行中 Child 时取消 Task，会先取消 Child，再取消 Parent；父操作稳定终止前 Task 保持
  `cancelling`。
- Child 或 Parent 的 `outcome-unknown` 会保留准确 Operation ID，并停止继续决策。
- ActionGateway 在入队前拒绝时，只在任务历史记录 `gateway.stale`、
  `gateway.lease-expired`、`gateway.forbidden` 或 `gateway.invalid` 等稳定类别；
  Provider 文本和内部错误详情不会进入任务历史。
- Provider 故障或预算耗尽会暂停而不是释放控制后遗留父 Macro；用户仍可恢复或取消 Task。

模型只能提出基于当前 Observation 和 Capability 的 ActionRequest。Host 仍负责绑定
目标、预览 Effect、执行 Policy、修改世界并返回 Outcome；人格、记忆或 Task Token
都不能授予世界权限。

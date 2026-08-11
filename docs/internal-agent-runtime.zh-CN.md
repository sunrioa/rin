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
    "actor_id": "actor.companion",
    "persona_id": "companion",
    "version": "v1"
  }]
}
```

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
- 状态文件使用私有权限、原子替换和单写者进程锁；配置不能改变状态路径。
- `scheduled=true` 只表示任务已进入后台队列，不表示模型已决策、游戏已执行或
  目标已经完成。
- 停止时先取消并等待 Agent worker，再释放 Task/Memory 锁，最后关闭 Control Plane。

模型只能提出基于当前 Observation 和 Capability 的 ActionRequest。Host 仍负责绑定
目标、预览 Effect、执行 Policy、修改世界并返回 Outcome；人格、记忆或 Task Token
都不能授予世界权限。

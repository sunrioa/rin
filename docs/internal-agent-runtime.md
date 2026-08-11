# Internal Agent Runtime

[English](internal-agent-runtime.md) | [简体中文](internal-agent-runtime.zh-CN.md)

The Internal Agent Runtime is an optional `rin-control` component. It advances
budgeted tasks with persona, memory, skills, and a model while every world read,
controller lease, policy decision, Operation, and authoritative outcome still
uses the shared Control Plane. Without an Agent configuration, `rin-control`
keeps its existing behavior.

## Identity boundaries

| Identity | Source | Authority |
| --- | --- | --- |
| Control Client | `RIN_CONTROL_PRINCIPAL` and `RIN_CONTROL_SCOPES` | `/control/v2` |
| Agent Client | Configured `client_principal` | Only `task.read`, `task.execute`, and `task.cancel` |
| Internal Runtime | Created in process and never exposed over HTTP | Only controls Actors with `DecisionAuthority=internal` |

`RIN_CONTROL_TOKEN` and `RIN_AGENT_TOKEN` should use different values. Neither
token can access the other route family. The Agent Client cannot receive
`host.admin`, `actor.*`, or game-specific scopes.

## Configuration

Create a JSON file readable only by the current user, such as
`/absolute/path/agent.json`:

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

The API key is not a configuration field; adding `api_key` to the JSON is
rejected. For an unauthenticated local OpenAI-compatible service, set
`authentication` to `none` and leave `RIN_AGENT_API_KEY` unset. Startup validates
configuration without sending a model probe request.

## State and calls

- [`api/agent-openapi.json`](../api/agent-openapi.json) is the Task HTTP contract.
- State is fixed at `<RIN_CONTROL_DATA_DIR>/agent/tasks.json` and `memory.json`.
- State files use private permissions, atomic replacement, and single-writer
  process locks. Configuration cannot redirect these paths.
- `scheduled=true` only means background coordination was queued. It is not
  proof of model deliberation, game execution, or task completion.
- Shutdown cancels and joins Agent workers before releasing Task and Memory
  locks, then closes the Control Plane.

The model can only propose an ActionRequest grounded in the current Observation
and Capability catalog. The Host still binds targets, previews Effects, applies
Policy, mutates the world, and reports the Outcome. Persona, memory, and a Task
token never grant world authority.

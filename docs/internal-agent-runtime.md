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
    "persona_id": "companion",
    "version": "v1"
  }]
}
```

A binding without `actor_id` is the explicit default for dynamically created
actors. Exact actor-and-controller and actor-only bindings take precedence.
Only one default is allowed, and it cannot select a controller.

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
- Task snapshots use `rin.cognition.tasks/v2`. This Preview version does not
  read v1; finish or cancel old internal tasks before upgrading rather than
  copying live Operation state.
- State files use private permissions, atomic replacement, and single-writer
  process locks. Configuration cannot redirect these paths.
- `scheduled=true` only means background coordination was queued. It is not
  proof of model deliberation, game execution, or task completion.
- Shutdown cancels and joins Agent workers before releasing Task and Memory
  locks, then closes the Control Plane.

## Macro parent-child loop

The internal Runtime and external MCP use the same parent-child Operation
contract. After the model selects a capability declared as `kind=macro` with
`produces_child_operations=true`, the Task records its `macro_operation_id`
only when the Host advances the parent to `accepted` or `running`. The next
observation carries a trusted `parent_operation_id`; every selected atomic
child still passes through Host binding, Policy, execution, and authoritative
Outcome reporting.

- `queued`, `delivered`, `awaiting-confirmation`, `accepted`, and `running` are not
  completion evidence. The parent remains in the Task until an authoritative
  terminal state.
- While a parent macro runs, the model sees only atomic capabilities. The
  Control Plane supports nested macros, but this Runtime does not create a
  second automatic parent level yet.
- Cancelling a Task with a running child cancels the child before the parent;
  the Task remains `cancelling` until the parent settles.
- An `outcome-unknown` child or parent retains the exact Operation ID and stops
  further decisions.
- A pre-queue ActionGateway rejection records only a stable class such as
  `gateway.stale`, `gateway.lease-expired`, `gateway.forbidden`, or
  `gateway.invalid`; provider text and internal error details do not enter task history.
- Provider failure or budget exhaustion pauses instead of releasing control
  and orphaning a parent macro; the Task can still be resumed or cancelled.

The model can only propose an ActionRequest grounded in the current Observation
and Capability catalog. The Host still binds targets, previews Effects, applies
Policy, mutates the world, and reports the Outcome. Persona, memory, and a Task
token never grant world authority.

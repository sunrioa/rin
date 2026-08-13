# Internal Agent Runtime

[English](internal-agent-runtime.md) | [简体中文](internal-agent-runtime.zh-CN.md)

The Internal Agent Runtime is an optional `rin-control` component. It advances
budgeted tasks with persona, memory, skills, and a model while every world read,
controller lease, policy decision, Operation, and authoritative outcome still
uses the shared Control Plane. Without an Agent configuration, `rin-control`
keeps its existing behavior.

The runtime currently advances explicitly created tasks. It does not create a
new background task merely because a persona has an `initiative_policy`. A game
may create tasks for a proactive greeting, checking player state, or continuing
an unresolved topic from trusted events. Initiative then constrains expression
and consecutive actions inside that task, preserving a visible trigger,
cooldown, and cancellation path.

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
  }],
  "learning": {
    "enabled": true,
    "publish_mode": "draft",
    "min_actions": 3,
    "adapter": "minecraft",
    "max_output_tokens": 1200
  }
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

## Persona, memory, and skills

`PersonaProfile` describes identity and presentation only: identity, traits,
values, voice, boundaries, relationship stances, initiative, and presentation
rules. It contains no scopes, policy rules, API keys, or executable hooks.
Bindings resolve in Actor+Controller, Actor, then default order.

Memory namespaces provide structural isolation:

| Domain | Visibility | Purpose |
| --- | --- | --- |
| `actor-episodic` | controllers of one Actor | shared experiences |
| `actor-semantic` | controllers of one Actor | stable preferences, promises, and relationship facts |
| `controller-working` | current controller | current task working memory |
| `controller-private` | current controller | private reasoning and externally hidden content |
| `controller-belief` | current controller | unverified hypotheses |

Each memory carries provenance, authority, confidence, importance, TTL,
subjects, tags, and superseded records. Model-created candidates are always
non-authoritative subjective records; Host outcomes provide authoritative world
evidence. Retrieval is bounded by record and character budgets instead of
sending all history to the prompt. Forget creates tombstones and consolidation
can replace several records with a sourced summary.

Default retrieval is fully offline. SQL first constrains candidates by session,
actor, controller, domain, tombstone, and expiry, then deterministically merges
recent candidates, `unicode61` FTS5/BM25, and trigram FTS5 for CJK substrings.
Queries shorter than three characters never invoke the trigram path. Record and
character budgets are applied after source ranks and existing memory scores are
merged. Wiki projections, relation graphs, rerankers, and background vector
services are not part of this path.

`rin-control` exclusively owns `<RIN_CONTROL_DATA_DIR>/agent/memory.db`.
SQLite is the online source of truth for the Rin Memory domain and uses WAL,
full synchronization, and FTS5. `memory.json` is imported only on the first
empty-database startup; JSONL is a manual interchange format. Actions initiated
by the internal Agent, external MCP, or a macro all reach the same projection,
and only a committed Host Outcome can create shared `actor-episodic` memory.
The `canon_ref` retains Host, World, Epoch, Sequence, and Digest evidence. It is
a searchable projection of game-owned Canon and cannot mutate Canon.

A skill is inert procedural guidance containing a summary, trigger tags,
instructions, and digest. It has no entrypoint, scope, or capability grant. The
model first sees summaries and may expand at most one skill. Instructions asking
for privileged behavior still cannot change the allowed capabilities, binding,
or policy.

The catalog belongs to `rin-control`, not privately to the Internal Runtime.
Configured built-ins, `skills/installed`, and `skills/learned` form one
deterministic catalog used directly by the internal Agent and exposed to MCP
through `skill.read` and `skill.write`. MCP skills remain available when the
internal model runtime is disabled.

An external MCP controller keeps its persona and private memory in the external
Agent. Internal persona does not override it and Rin does not automatically copy
that private state into Internal Agent memory.

`learning` is disabled by default. When enabled, only a completed task with at
least `min_actions` actions and an authoritative successful Host Outcome makes
one additional model call. The default `publish_mode=draft` writes below
`skills/drafts` and does not expose the result to the active catalog. Explicit
`learned` mode writes below `skills/learned`. Draft input omits operation IDs,
Host references, coordinates, world UUIDs, and credentials. Rin derives adapter
and capability applicability from evidence instead of trusting the summarizing
model. Learning failure never changes the task result.

## Model decisions

One model response is exactly `action`, `wait`, `complete`, or `inspect`:

- `action` selects one allowed capability, strict JSON arguments, and listed target handles;
- `inspect` expands at most four capabilities and one skill for one round;
- `wait` means there is no grounded action now;
- `complete` still requires the runtime to verify the goal through observation or outcome.

The trusted contract is separate from `untrusted_context`. Persona, memory,
skills, observation, player text, and capability descriptions are untrusted
data and cannot alter the allowed set, epoch, controller, or budgets.

## State and calls

- [`api/agent-openapi.json`](../api/agent-openapi.json) is the Task HTTP contract.
- State is fixed at `<RIN_CONTROL_DATA_DIR>/agent/tasks.json` and `memory.db`.
- Task snapshots use `rin.cognition.tasks/v2`.
- Task files use private permissions and atomic replacement. Memory uses SQLite
  transactions, WAL, and a single-writer process lock. Configuration cannot
  redirect these paths.
- `scheduled=true` only means background coordination was queued. It is not
  proof of model deliberation, game execution, or task completion.
- `allowed_capabilities` is an optional task-local allowlist of at most 128
  capability IDs. When non-empty, the Runtime exposes only its intersection
  with the current Host catalog and revalidates restored pending actions. An
  empty array uses the current full Host catalog. This field can only narrow
  authority; it cannot create capabilities or bypass Policy.
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
  second automatic parent level yet. A task-local allowlist must name both the
  parent Macro and its expected children; entering the Macro phase never
  expands task authority.
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

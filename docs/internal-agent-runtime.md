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

`RIN_CONTROL_TOKEN` and `RIN_AGENT_TOKEN` must use different values. Neither
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
  "memory": {
    "semantic_embedding": {
      "enabled": true,
      "provider": "openai-compatible",
      "base_url": "https://api.example.com/v1",
      "model": "example-embedding-model",
      "authentication": "bearer-env",
      "allowed_domains": ["actor-episodic", "actor-semantic"],
      "min_local_matches": 4,
      "max_semantic_results": 4,
      "timeout_millis": 1500
    }
  },
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
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="local.player"
export RIN_AGENT_TOKEN="$(openssl rand -hex 32)"
export RIN_AGENT_API_KEY="provider-key-from-secret-store"
export RIN_AGENT_EMBEDDING_API_KEY="embedding-key-from-secret-store"
./bin/rin-control
```

API keys are not Agent configuration fields; adding `api_key` to that JSON is
rejected. As an alternative to the environment variables above, the loopback
Rin Console can store model and embedding keys in
`<data>/agent/agent-secrets.json`. The file is mode `0600`, responses expose
presence only, and environment variables override local values. For an
unauthenticated local OpenAI-compatible service, set
`authentication` to `none` and leave `RIN_AGENT_API_KEY` unset. Startup validates
configuration without sending a model probe request.

For providers such as DeepSeek that support JSON objects but not JSON Schema as
a response format, set `response_format` to `json_object`. Rin includes the
schema in the stable system message and still validates the returned object
locally. `thinking_mode` is optional; set it to `enabled` or `disabled` only
when the selected OpenAI-compatible provider implements that request field.
For DeepSeek V4 Flash, use `base_url: https://api.deepseek.com`,
`model: deepseek-v4-flash`, `response_format: json_object`, and
`thinking_mode: disabled` for low-latency action decisions. This is only an
adapter configuration example; Persona, Memory, Skill, the Agent Loop, Control
Plane, and Host contracts do not depend on that provider or model name.

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

Remote semantic recall is an optional supplement. It is disabled unless
`memory.semantic_embedding.enabled=true`, an OpenAI-compatible endpoint and
model are configured, and `allowed_domains` explicitly permits a non-private
memory domain. Only complex planned tasks request semantic recall, and only
when local recall is insufficient. Document embeddings are generated on a
bounded background queue and stored in the rebuildable `memory_embeddings`
table keyed by configured model and content digest. Query vectors use a small
process-local cache; results are rechecked against current visibility, expiry,
filters, and content digest before use.

The embedding key is separate from JSON configuration. It may come from
`RIN_AGENT_EMBEDDING_API_KEY` or the Console-managed secret file; the environment
variable takes precedence. Private controller domains and text resembling
credentials are never sent to the embedding endpoint. Timeout, transport
failure, rate limiting, invalid model, invalid dimensions, or malformed vectors
fall back to normal FTS5 and recent memory results. Rin does not download or run
an embedding model, and the remote provider never owns Memory IDs, facts,
permissions, or deletion.

`rin-control` exclusively owns `<RIN_CONTROL_DATA_DIR>/agent/memory.db`.
SQLite is the only online source of truth for the Rin Memory domain and uses WAL,
full synchronization, and FTS5. JSONL is an explicit manual interchange format;
there is no parallel JSON persistence backend. Actions initiated
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

Each completion request places the fixed protocol first, followed by one
deterministically serialized static-context message containing the decision
schema digest, Persona, sorted Capability summaries, and sorted Skill summaries.
Task ID, goal, Observation, Epoch, targets, retrieved memory, inspection output,
and PlanState remain in the final dynamic message. This byte-stable prefix lets
compatible providers reuse their own prompt cache without a Rin cache service.
Changing Persona, a Capability spec digest, a Skill digest, or the decision
schema changes the private `stable_prefix_digest` recorded with the request.

The OpenAI-compatible adapter maps provider-reported cache hit, miss, and write
tokens, including common compatible aliases, into `provider.Usage`; the task
timeline records only those measured values. Missing fields remain unknown, not
zero. Rin does not cache ActionRequest, Observation, Policy decisions, or world
outcomes and sends no provider-specific cache parameter by default.

When a provider must translate the generic response schema into prompt text,
it returns the final messages through the optional request-preparation
interface. Rin then checks context size and computes request and stable-prefix
digests. Providers that need no transformation implement nothing, while the
resilience wrapper only delegates the preparation contract.

One model response is exactly `action`, `wait`, `complete`, or `inspect`:

- `action` selects one allowed capability, strict JSON arguments, and listed target handles;
- `inspect` expands at most four capabilities and one skill for one round;
- `wait` means there is no grounded action now;
- `complete` requests acceptance under the caller-owned completion policy; it cannot override that policy.

The trusted contract is separate from `untrusted_context`. Persona, memory,
skills, observation, player text, and capability descriptions are untrusted
data and cannot alter the allowed set, epoch, controller, or budgets.

## State and calls

- [`api/agent-openapi.json`](../api/agent-openapi.json) is the Task HTTP contract.
- State is fixed at `<RIN_CONTROL_DATA_DIR>/agent/tasks.db` and `memory.db`.
- Tasks use SQLite schema version 2 and the `rin.cognition.tasks/v5` projection.
  The first database open imports an existing `tasks.json` v3, v4 or v5 snapshot;
  later opens use the database, even if that obsolete backup changes.
- Task CAS writes one task row and the snapshot revision in one SQLite transaction.
  WAL, `synchronous=FULL`, private files and a single-writer process lock preserve
  the durable-return guarantee. A failed commit blocks cache reads until reopen.
  Configuration cannot redirect these paths. See [migration](execution-storage.md).
- `scheduled=true` only means background coordination was queued. It is not
  proof of model deliberation, game execution, or task completion.
- `allowed_capabilities` is an optional task-local allowlist of at most 128
  capability IDs. When non-empty, the Runtime exposes only its intersection
  with the current Host catalog and revalidates restored pending actions. An
  empty array uses the current full Host catalog. This field can only narrow
  authority; it cannot create capabilities or bypass Policy.
- The daemon stops Agent workers and closes Task state first, then stops Control
  Plane delivery workers before closing the shared Plan and Memory stores.

## Scheduling and cancellation

`TaskSession.schedule` is durable control state. Task history remains diagnostic;
appending a warning or a Signal event cannot change whether the task runs.

| `schedule.kind` | Resume condition |
| --- | --- |
| `ready` | Queue a bounded runtime advance. |
| `waiting-observation` | The Actor is online and publishes a newer sequence or a different epoch. |
| `waiting-operation` | The Operation cursor changes, the Operation settles, or reconciliation becomes necessary. |
| `waiting-confirmation` | The tracked Operation changes after confirmation or cancellation. |
| `retry-at` | The persisted retry deadline has elapsed. |
| `waiting-user` | An explicit run or resume request, as appropriate to task status. |
| `stopped` | Deliberation stops; an unknown outcome still observes its original Operation for reconciliation. |

A model `wait` records the current observation epoch and sequence. Operation and
confirmation waits record an Operation ID and cursor, then release the worker.
Task and Control Plane invalidations select indexed active tasks by world, Actor
or Operation, without copying historical task context. A periodic scan recovers from
missed notifications or process restart. The default recovery scan interval is five seconds
and transient failures retry after five seconds. A planned task also waits until
the `task-plan` outcome subscriber acknowledges its projection; Memory delivery
never gates readiness. `OperationWaitMillis` is retained for source compatibility
but no longer controls worker waiting.

Importing a v3 snapshot infers a legacy observation wait once. A legacy wait with
no recorded observation requires an explicit run. v4 and v5 snapshots must carry
a valid schedule; legacy tasks default to model-declared completion. The original
JSON is a migration backup, not a live replica for an older binary.

Cancellation persists `cancel_requested` before cancelling the active run's
context and does not wait for the per-task execution lock. Late model responses
are discarded. `action_submission_started` records when a pending intent may
have reached the gateway. Recovery resolves that exact intent through the
process-local `FindActionOperation` port and never submits it from the cancellation
path. If the intent may have been submitted but no Operation can be found, the
task becomes `outcome-unknown` with `action.submission-unknown`; absence alone is
not proof that no effect occurred.

A cancellation request does not undo effects already applied by the Host. Pending
Host operations remain `cancelling` until authoritative settlement. Custom model
providers and outcome subscribers must honor context cancellation to release
their own resources promptly.

## Independent completion criteria

`StartTaskInput.completion` controls acceptance independently of `planning_mode`:

| `completion.mode` | Completion requirement |
| --- | --- |
| `model-declared` (explicit) | The model requests completion and any existing Plan is complete. |
| `host-evidence` | The model requests completion and all caller-supplied conditions have Host evidence. |
| `human-confirmation` (new-task default) | The model requests review; a caller with `task.execute` accepts the exact task revision. |

For `host-evidence`, supply 1–16 conditions using the Plan condition shape. An
`observation-fact` matches an exact scalar `fact_value_json`; all fact conditions
must hold in the same current observation. An `operation-outcome` names an exact
Capability ID/version and requires a confirmed successful operation from this
task in the current epoch. Unrelated actions, past-epoch results, and accumulated
facts from different snapshots do not satisfy acceptance. The model cannot rewrite
the caller's criteria. Missing evidence registers an observation wait; new evidence
can finish an already requested completion without another model call.

```json
{"mode":"host-evidence","conditions":[{"condition_id":"goal.arrived","kind":"observation-fact","summary":"The Host confirms arrival.","fact_id":"actor.at-destination","fact_value_json":"true"}]}
```

Existing tasks preserve their recorded policy; legacy snapshots without one keep
model-declared semantics. Short-lived automatic initiative explicitly selects
model-declared acceptance. Caller goals default to human confirmation.

`completion.operation_requirements` optionally tightens outcome conditions by
`condition_id`. `arguments_json` matches the complete argument object (key order
is ignored; numeric spelling is exact). `target_refs` requires exact references
from the Host's resolved binding, including Epoch. `minimum_count` counts 1–64
distinct successful Operation IDs; retries of one ID cannot increment it.
Requirements are immutable for the task. These count operations, not item totals.
For duration, inventory totals or compound world goals, the Host should publish a
scalar acceptance fact only after evaluating the complete goal, for example
`goal.bridge-held-30s=true` or `goal.has-ten-wood=true`.

```json
{"mode":"host-evidence","conditions":[{"condition_id":"goal.collect","kind":"operation-outcome","summary":"Collect wood twice.","capability":{"id":"game.item.collect","version":"1.0.0"}}],"operation_requirements":[{"condition_id":"goal.collect","arguments_json":"{\"item\":\"wood\"}","minimum_count":2}]}
```

Human review pauses with `completion.confirmation-required`. Submit
`POST /agent/v1/tasks/confirm-completion` with `task_id` and `expected_revision`.
A stale revision returns conflict; cancellation, a pending action or an incomplete
Plan cannot be overridden. Resume returns the task to deliberation. The Console
exposes the completion mode when creating a task and an acceptance control while
the task awaits human review.

The runtime separates context collection, bounded model decisions and decision
application. Explicit `inspect` and argument-schema repair share one additional
model-call budget while preserving their distinct diagnostic events.

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

## Recovery and settled history

Unknown outcomes stop model decisions but keep reconciling the original intent or
Operation, including a late Host result and the required Plan projection. A Run
request is also a read-only reconciliation entry while the result remains unknown.
Reconciliation does not resubmit the action. Once the original operation settles,
the task can continue or complete cancellation; it no longer permanently blocks
its Actor's Signal coordinator.

Plan creation uses `plan.<TaskID>`. If Plan commit succeeds before the Task reference
is saved, restart adopts the existing plan after checking Task, Host, world, Actor,
controller, session, goal and planning ownership. The plan's saved Epoch still
governs evidence and action authorization; a changed Epoch requires revalidation
or replanning. Cancellation also looks up an unreferenced owned plan. See
[storage retention and migrations](execution-storage.md) for Task/Plan archives,
Signal durability and incremental decision records.

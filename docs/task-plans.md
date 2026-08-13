# Task Plans

Rin Task Plan v1 is an optional, engine-neutral progress record for work that
needs several authoritative game actions. It is not a second policy engine and
does not execute actions.

Use `planning_mode=disabled` for simple work. In `auto` mode an Agent may attach
a coarse plan to its first structured decision; `required` rejects a decision
until it provides one. Planning is part of that same model response, so Rin does
not add a Planner request before every step.

Each action associated with a plan carries `plan_step_ref`. The Task Coordinator
checks the exact plan revision and active step, then submits the ordinary typed
ActionRequest through Host binding, Policy, and Operation. Only an authoritative
Host Outcome, a current Observation Fact, player confirmation, or a Host
condition can satisfy a declared condition. Model text cannot advance a step.

Internal and external Agents share `taskstate.db`. The internal runtime stores
only `plan_id`, revision, and current step in its TaskSession. MCP exposes
create/get/wait/revise, pause/resume/cancel, transition, and step-action tools;
it never invokes the internal model or adopts its Persona.

Plans are bounded to 16 steps, use compare-and-swap revisions, permit one active
plan per actor, and allow one unfinished Operation per plan. A restart restores
progress but does not restore authority: the current controller lease, Epoch,
observation, capability, and Policy are checked again before any action.

The HTTP contract is `api/task-plan-openapi.json`; language-neutral requests are
in `api/task-plan-v1-fixtures.json`.

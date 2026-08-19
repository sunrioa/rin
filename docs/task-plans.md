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
Host Outcome or a current Observation Fact can satisfy a declared condition.
Player or model text cannot advance a step without one of those Host-owned
records.
Every `operation-outcome` condition names one exact capability, and every
`observation-fact` condition names one exact Host fact ID and expected scalar
value. A successful but unrelated action or mismatched fact cannot advance the step. Plans do not retain decorative
`preconditions`; preparation belongs in the step objective and Skills, while
machine progress uses only verifiable success conditions.

Internal and external Agents share `taskstate.db`. The internal runtime stores
only `plan_id`, revision, and current step in its TaskSession. MCP exposes
create/get/wait/revise, pause/resume/cancel, transition, and step-action tools;
it never invokes the internal model or adopts its Persona.

Plans are bounded to 16 steps, use compare-and-swap revisions, permit one active
plan per actor, and allow one unfinished Operation per plan. A restart restores
progress but does not restore authority: the current controller lease, Epoch,
observation, capability, and Policy are checked again before any action.

Three consecutive failures from the same authoritative Outcome family permit a
revision even when the current step has a higher `max_attempts`; that step limit
still determines when the step becomes blocked. If another MCP or Principal has
taken control before submission, the internal task discards its unsubmitted
action and pauses as `controller.contended`. It does not skip the step, cancel
the plan, or retry every five seconds. After the external controller releases
the actor, an explicit resume causes a fresh observation before another action.

Rin Console merges internal TaskSessions with the shared PlanStore. Plans created
only through MCP show their phase, conditions, evidence, revision, and controller
source without exposing internal task run, resume, or cancel buttons; their owner
continues to manage them through MCP.

The HTTP contract is `api/task-plan-openapi.json`; language-neutral requests are
in `api/task-plan-v1-fixtures.json`.

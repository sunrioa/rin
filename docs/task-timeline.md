# Task timeline

`rin.task-timeline/v1` is a bounded, read-only explanation surface shared by
the internal Agent Runtime and external MCP controllers. It observes existing
task and operation state; it never grants authority, advances a task, or
executes an action.

## What it records

- public task status and short caller-visible summaries;
- observation sequence, epoch, and selected capability;
- measured model latency and token usage when a provider reports them;
- IDs and digests for authorized Skill and memory context, never their text;
- policy disposition, public reason, matched rule IDs, and effect count;
- operation delivery, progress, terminal outcome, and execution proof.

The timeline never automatically projects provider prompts, hidden reasoning,
configured credentials, raw model responses, memory text, Skill text, action
arguments, or private Host output. The caller-authored goal and producer-authored
public summaries remain visible; their producers must keep secrets out of those
explicitly public fields.
`queued`, `delivered`, `accepted`, and `running` are not proof of execution.
Only terminal `succeeded` evidence with `execution_confirmed=true` proves that
the authoritative Host reported completion.

## Reading a timeline

Use the local daemon CLI:

```sh
RIN_CONTROL_TOKEN='<local-token>' rin tasks timeline <task-id> --follow
```

Add `--json` to write one contract page per line. A consumer resumes from the
opaque `next_cursor`; it must not parse the cursor as a timestamp or database
offset. `truncated=true` means older retained evidence is no longer available.

MCP clients use `get_task_timeline` for the initial page and
`wait_task_timeline` for bounded long polling. `changed=false` means only that
no newer evidence arrived before the wait ended.

Internal Agent clients use `/agent/v1/tasks/timeline/get` and `/wait`. External
controllers and all language SDKs use `/control/v2/tasks/timeline/get` and
`/wait`. Access remains scoped to the task owner; Host administrators may read
Control timelines for diagnostics.

The frozen internal-Agent and external-MCP examples are in
[`api/task-timeline-v1-fixtures.json`](../api/task-timeline-v1-fixtures.json).
The behavioral baseline tests additionally freeze these complete event orders:

- internal Agent: `task.created` -> `model.decision` -> `action.selected` ->
  `operation.submitted` -> `operation.terminal` -> `model.decision` ->
  `task.completed`;
- external MCP: `operation.queued` -> `operation.delivered` ->
  `operation.accepted` -> `operation.succeeded`.

# 任务时间线

`rin.task-timeline/v1` 是内部 Agent Runtime 与外部 MCP Controller 共用的有界、
只读解释界面。它只投影已有 Task 和 Operation 状态，不会授予权限、推进任务或执行行动。

## 记录内容

- 公开任务状态和给调用方看的简短说明；
- Observation 序号、Epoch 和选中的 Capability；
- Provider 确实返回的模型延迟与 Token 用量；
- 已通过访问控制的 Skill、Memory ID 与摘要，不包含正文；
- Policy 结论、公开原因、命中规则 ID 和 Effect 数量；
- Operation 的投递、进度、终态 Outcome 与执行证明。

时间线不会自动投影 Provider Prompt、隐藏推理、已配置凭据、模型原始响应、Memory 正文、
Skill 正文、行动参数或 Host 私有输出。调用方提交的 Goal 和生产者明确标记为公开的摘要
仍然可见，因此生产者必须保证这些公开字段不含秘密。`queued`、`delivered`、`accepted`、
`running` 都不能证明行动已经执行；
只有终态为 `succeeded` 且 `execution_confirmed=true`，才能证明权威 Host 已报告完成。

## 查看时间线

通过本地 Daemon CLI 查看：

```sh
RIN_CONTROL_TOKEN='<本地令牌>' ./bin/rin tasks timeline <task-id> --follow
```

增加 `--json` 后，每行输出一个契约页面。调用方使用不透明的 `next_cursor` 续读，不能把
Cursor 当作时间戳或数据库偏移解析。`truncated=true` 表示更早的保留证据已经不可用。

MCP 客户端先调用 `get_task_timeline`，再通过 `wait_task_timeline` 有界长轮询。
`changed=false` 仅表示等待期间没有新证据，不能据此宣称执行成功。

内部 Agent 客户端使用 `/agent/v1/tasks/timeline/get` 与 `/wait`；外部 Controller 和全部
语言 SDK 使用 `/control/v2/tasks/timeline/get` 与 `/wait`。访问权限仍绑定任务所有者，
Host 管理员可为诊断读取 Control 时间线。

内部 Agent 与外部 MCP 的冻结样例位于
[`api/task-timeline-v1-fixtures.json`](../api/task-timeline-v1-fixtures.json)。
行为基线测试还冻结了两条完整事件顺序：

- 内部 Agent：`task.created` -> `model.decision` -> `action.selected` ->
  `operation.submitted` -> `operation.terminal` -> `model.decision` ->
  `task.completed`；
- 外部 MCP：`operation.queued` -> `operation.delivered` ->
  `operation.accepted` -> `operation.succeeded`。

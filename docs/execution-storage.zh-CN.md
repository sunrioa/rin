# 执行状态存储与迁移

[English](execution-storage.md) | [简体中文](execution-storage.zh-CN.md)

执行状态位于 `RIN_CONTROL_DATA_DIR`。SQLite 使用 WAL、`synchronous=FULL`、私有文件和
单进程写锁；低频配置继续使用 JSON。权威写入在事务提交后才返回成功；多个数据库与游戏
Host 的执行并不因此成为一个全局事务。

| 存储 | Schema | 内容 |
| --- | --- | --- |
| `agent/tasks.db` | 2 | 有界工作集、已结束任务归档、快照 Revision |
| `operations.db` | 2 | 工作集、终态归档、结果投递积压，以及 Policy/租约/急停检查点 |
| `agent/signals.db` | 1 | 有界收件箱、配置、冷却、游标、已接收事件及投递状态 |
| `agent/decision-records.db` | 1 | 有界决策诊断行与 Revision |
| `agent/taskstate.db` | 2 | Plan、步骤、事件、操作证据及状态索引 |
| `agent/memory.db` | 既有 Memory Schema | 共享及隔离记忆、检索索引 |

## 工作容量与证据保留

Task CAS 更新一个任务行和快照 Revision。工作集达到配置上限（默认 1,024）时，新建任务
会在同一事务中归档最早结束的任务并写入替代任务。未知结果、待处理 Action/Macro 和待完成
Skill 学习不能归档。旧 ID 继续保留，`GetTask` 与 Console 归档查询可读取历史；归档任务
不能重新激活，但允许迟到的学习检查点更新终态记录。Plan 容量只统计 planned、active、
blocked、paused；已关闭计划保留身份和证据，可通过 Plan ID 或 Task ID 查询。

Operation 的变更行与 Policy、Controller、急停和游标检查点共同提交。已确定终态且没有
活动子操作的记录，在超过保留期或工作集需要空位时归档；归档写入与工作集删除处于同一
事务。按 Operation ID 查询和精确动作幂等查询仍能找到归档结果。`outcome-unknown`
持续保留在工作集，直至 Host 补齐权威结果。

结果投递积压独立于执行容量。Outcome 与订阅者待投递条目共同提交；订阅者 ACK 与积压
删除也共同提交，Operation 归档后仍然如此。每个订阅者独立重试，失败次数和重试时间会
持久化。移除订阅者不会删除其待处理证据；恢复同一 ID 即可继续。新增订阅者会接收启动时
工作集中仍保留的结果，不会自动回填全部归档历史。

Console 执行页显示积压数量、最早等待时间、订阅者、尝试次数及配置是否存在。
`GET /management/v1/outcomes/backlog` 返回最早 100 条；
`POST /management/v1/outcomes/retry` 使用 `operation_id` 和 `subscriber` 安排补投。
重试只发送既有结果，不重新提交游戏动作。两者沿用管理 API 授权。
内存和旧 JSON Control 存储继续采用原有有界保留规则；独立持久积压由默认 SQLite 后端提供。

Task、Operation 工作投影仍受 64 MiB 逻辑上限约束，新 Operation 准入预算为 32 MiB。
Task/Plan/Operation 冷历史默认永久保留，不占工作容量；没有自动破坏性清理。需要按实际
历史量准备磁盘空间并备份完整数据目录。这些限额不代表数据库文件或 WAL 的物理大小上限。

决策诊断按行追加并淘汰最旧记录，默认最多 4,096 条、64 MiB，不再每次重写完整 JSON。
Signal 保留逐角色容量、Epoch 和 TTL 规则；重启后仅接续已接收且未过期事件，总收件箱
预算为 64 MiB。达到重试上限或过期仍会结束该短期提示；重要长期目标应创建持久 Task。

Task/Signal/决策记录提交失败时不暴露未提交的缓存成功；决策提交结果不确定时需重开
Recorder。Operation 写入失败后重新读取持久行身份，再按既有语义重试。投递次数和
ActionRun 进度继续采用检查点语义；新请求、ACK、取消、Outcome 与订阅者 ACK 同步提交。

## 升级与回退

1. 停止守护进程，备份完整 `RIN_CONTROL_DATA_DIR`。
2. 启动新版。新 Task 数据库导入 `tasks.json` v3/v4/v5/v6，Operation 导入
   `operations.json` v5/v6，决策记录导入 `decision-records.json` v1。既有 Task、
   Operation Schema 1 数据库补充归档和积压表。首次升级没有旧持久 Signal 来源，因此
   收件箱从空开始；后续重启可恢复。
3. 原 JSON 原样保留。数据库提交后即为后续打开时的权威来源。未完成的迁移事务可重试；
   损坏状态和不支持的版本会使启动失败。旧文件接口拒绝同名已迁移 `.db`；Task 与决策
   导入也持有旧 JSON 的写锁。

调用方新建任务默认采用人工验收，已有任务保留原策略。短期自动主动任务显式使用
模型声明完成。详见[独立验收](internal-agent-runtime.zh-CN.md#独立目标验收)。

备份时先停机或使用 SQLite 支持的备份机制；复制运行中的 `.db` 却忽略 WAL 并不完整。
回退需停止新版、整体恢复升级前目录，不能混用旧 Task JSON 与较新的 Operation、Policy、
Plan、Signal 或 Memory 状态。

## 可复现测量

```sh
go test ./cognition ./controlplane -run '^$' -bench 'Benchmark(Task.*CAS|Operation.*Commit)' -benchtime=200ms -count=2
go test ./cognition -run '^$' -bench BenchmarkTaskLoadUnderWrites -benchtime=200ms -count=2
go test ./cognition -run '^$' -bench 'Benchmark(DecisionPersistence|TaskSchedulingSelection)$' -benchtime=200ms -count=2
```

以上为本机存储和选择查询微基准，不计初始化和迁移。Task 每条保留 64 个事件，Operation
每条约 8 KiB 结果。决策记录预载 1,000 条后测量追加；调度比较复制 1,000 个任务历史与
索引查询一个角色。它们不代表模型延迟、Host 吞吐量或端到端任务性能。

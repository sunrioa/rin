# 执行状态存储与迁移

[English](execution-storage.md) | [简体中文](execution-storage.zh-CN.md)

守护进程在 `RIN_CONTROL_DATA_DIR` 下使用 `agent/tasks.db` 和 `operations.db`。
两者均为 SQLite Schema 1，启用 WAL、`synchronous=FULL`、私有文件与原有单写者锁。
低频配置仍使用 JSON。

Task CAS 只提交一个任务行和快照 Revision。调度读取使用不可变缓存投影；提交失败或结果
不确定时，重新打开存储前禁止读取该缓存。Operation 只重写变更行，Policy 状态、Controller
租约、急停、全局时间线游标以及各订阅者的结果确认处于同一事务。写入失败后，重试前重新读取
数据库行标识，以清理可能已提交但调用未获确认的孤立行。原有 64 MiB 逻辑状态限制与新操作
32 MiB 准入预算继续生效；数据库文件及 WAL 还包含 SQLite 的额外开销。

投递次数和 ActionRun 进度仍采用原有检查点语义，在下一次权威提交或正常关闭时一起保存。
新请求、ACK、取消、Outcome 和订阅者确认仍同步落盘。SQLite 不会让 Host 执行与 Rin
自动成为跨进程事务。

## 升级

1. 停止守护进程，备份完整 `RIN_CONTROL_DATA_DIR`。
2. 启动新版程序。新任务数据库导入 `tasks.json` v3/v4/v5；新操作数据库导入
   `operations.json` v5/v6。先校验和规范化，再提交迁移事务，包括调度、待取消状态、
   Policy 预留和结果投递记录。
3. 原 JSON 原样保留作为备份。数据库提交成功后，后续打开以数据库为准，修改旧 JSON
   不会使运行状态回退。

迁移事务未提交时，中断后可以重试；源状态损坏或版本不支持会使启动失败。Task 导入同时
持有 JSON 与 SQLite 写锁，Operation 沿用同一目录锁。当前旧文件打开接口会拒绝已有同名
`.db` 的路径，避免误用过期快照。

备份 SQLite 时应先停机，或使用 SQLite 支持的备份机制；只复制运行中的 `.db` 而忽略
WAL 并不完整。旧版程序不能接续这些数据库。回退需停止新版并整体恢复升级前备份，
不能混用旧 Task JSON 和较新的 Operation、Policy、Plan 或 Memory 状态。

## 可复现测量

```sh
go test ./cognition ./controlplane -run '^$' -bench 'Benchmark(Task.*CAS|Operation.*Commit)' -benchtime=200ms -count=2
go test ./cognition -run '^$' -bench BenchmarkTaskLoadUnderWrites -benchtime=200ms -count=2
```

这是本机存储微基准：每个 Task 保留 64 条事件，每个 Operation 保留约 8 KiB 结果内容，
每轮仅更新一个已有记录。初始化和 JSON 导入不计入测量；结果不代表模型延迟、Host 吞吐量
或生产任务的端到端性能。
第二个命令测量持续写入期间的 Task 读取延迟，包含锁竞争和防御性复制成本，并非纯粹的锁等待时间。

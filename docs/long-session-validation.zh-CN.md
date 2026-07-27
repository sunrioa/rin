# 长会话存储与加速一年验证

[English](long-session-validation.md) | [简体中文]

Rin 不会把 NPC 的完整生命周期塞进应用日志或一个不断增长的 Prompt，而是区分
四类存储：

| 数据 | 保留与检索 |
| --- | --- |
| 权威 Event 与永久 Request/Event Identity | 每个 Session Append-only；通过分页 Timeline/Replay 或有界 Session Transfer 读取 |
| Runtime Memory Projection | 启用 `memory-archive-v1` 后保留固定详细窗口，并确定性生成 Summary |
| 可选语义检索 | 可丢弃 `MemoryIndex`；重建或删除不会修改权威 History |
| 运维日志/Telemetry | 不含内容的 Request 与 Lifecycle Metadata；保留由外部日志轮转负责 |

随附 File Store 会 gzip 压缩可重建 Checkpoint，同时让权威 Hash-chained
Event Log 保持普通 JSONL。

Archive Session 会冻结 Event Chain Anchor 并把 Session 设为只读，但不会重写、
截断或静默删除权威 Event。容量监控使用经鉴权的 Session Stats，备份/迁移使用
有界 Export；需要移除数据时按文档执行 Archive-then-delete 与 Tombstone Policy。

## 自动化加速一年负载

`TestAcceleratedYearSession` 让一个 NPC 经历：

- 365 个模拟日；
- 每 6 个模拟小时一次 Observation（共 1,460 次）；
- 每个模拟日一次 Proposal 与 Terminal Outcome；
- 每月 Snapshot；
- 自动 Checkpoint；
- 通过 `Engine.Close`、File Store Close 和重新打开模拟进程重启；
- Timeline 尾部检索和精确 Revision 1,000 Replay；
- Storage Stats 与最终 Session Archive。

测试会确认：权威 Revision/Head 在重启后不变，详细 Memory 有界，较旧 Memory
形成 Summary，Event/Index/Snapshot 字节统计非零，历史查询仍有效，并且关闭
Store 前会排空 Checkpoint Worker。

完整 365 天容量测试作为独立普通测试门禁运行。Race Build 会排除这一项磁盘容量
测试，以保持在 Go 标准测试超时内；完整 Race Suite 仍覆盖 File Store Artifact
并发，以及 Engine Operation、Transfer 和 Checkpoint 关停路径。

这是确定性的容量与生命周期回归，不是一年真实墙钟 Soak、游戏帧预算 Benchmark、
Provider 可用性承诺，也不能证明某个具体 Mod Loader。真实 Host 门禁仍要求每个
声称支持的 Host/Backend 至少 1,000 Turn 或两小时，并注入进程强杀、网络故障
与游戏存档。

## 生产容量规划

增长由互动频率和 Payload 大小决定，不能只看日历时间。发布持久 NPC 前：

1. 使用游戏的代表性 Event 频率与 Payload 分布测试；
2. 监控 `event_log`、`indexes`、`checkpoints`、`snapshots` 与总字节数；
3. 使用分页 Timeline/Replay，不要一次加载完整 Lineage；
4. 不把 Provider Prompt、原始音频或完整玩家文本写入运维日志；
5. 定义 Backup、Archive、Delete 与外部日志轮转策略；
6. 每条卸载路径都必须在关闭 Store 前调用 `Engine.Close(ctx)`。

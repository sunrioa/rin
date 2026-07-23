# BepInEx Rin NPC 示例

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Rin 智能体运行时的最小插件接入模板。

该源码覆盖层面向现代 Unity/.NET 运行时上的 BepInEx 6。

1. 使用官方 BepInEx Plugin Template，为目标游戏的 Backend 和 Framework
   版本创建插件。
2. 添加对 `sdk/csharp/Rin.Client/Rin.Client.csproj` 的项目引用，或把编译
   后 Assembly 复制到插件引用目录。
3. 添加 `Plugin.cs`，启动 Rin，并把插件构建到 `BepInEx/plugins`。
4. 只在生成的 BepInEx Config 中配置 `BaseUrl`。远程 Bearer Token 通过
   `RIN_TOKEN` 进程环境变量提供。
5. 按 F8 运行隔离 Demo Turn，或从目标游戏真实对白/交互 Hook 调用
   `RequestNpcTurn`。

`Update` 只排空有界主线程队列并检测可选 Demo Key；HTTP 异步运行。插件
启用 `outcome-reporting-v1`，并在应用前重新读取 Session。Proposal 必须仍
是 `pending`，而且 World Revision（非世界 Proposal 则为创建 Revision）
仍然匹配，否则游戏不执行效果而报告 Rejected。插件验证 `talk`、`wait`
或 `refuse`，在 Unity 主线程调用 `NpcActionReady`，并在实际 Accept/Reject
时读取 `Time.frameCount`。真实游戏插件应把这些 ID 映射到自己的 NPC API。

完整 Create Payload、Request ID 和 Seed 在所有重试间保持不变。只有 Rin
尚未生成在线 Proposal 的冷启动不可用场景，插件才执行一个明确由游戏编写
的 Fallback；在线 Proposal 后的 State 失败必须 Fail Closed。只要 Outbox
仍有 Entry，就禁止开始新 Turn。插件会在提交前保留完整 Propose Request，
并在收到 `202` 后立即保存 Job ID；未决 Attempt 会在下一次交互中用同一
身份恢复，不增长 Sequence，也不选择 Fallback。只有游戏效果、Applied
Marker 与 Outbox 在同一事务中落盘时才移除 Attempt；未决 Attempt 或 Outbox
都会阻止所有新 Turn。

本源码示例只在内存保存 Applied Operation 与 Outbox。每条 Commit 也保存
一个只含 Memory 与绝对 Fact 的安全 Observe 降级载荷；临时错误保留原
Commit，只有 `unknown_proposal` 等明确终态错误才原子转换。Durable
ACK/Delete 成功后才能 Evict。生产接入应把标记 Hook 替换为可失败的游戏
保存事务，同时包住效果、Marker、两份报告载荷、保留的 Create/Propose
Request、可选 Job ID 以及 Session/Sequence 状态。
尤其是 `NpcActionReady` Subscriber 抛错时必须回滚，不能留下 Accepted
Marker 或 Outbox；只有目标游戏的真实事务还能撤销 Subscriber 已部分写入
的世界状态。

官方插件教程：https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/index.html

配置指南：https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/4_configuration.html

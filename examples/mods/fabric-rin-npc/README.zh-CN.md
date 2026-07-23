# Fabric Rin NPC 示例

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Rin 智能体运行时的最小服务端接入模板。

该目录是源码覆盖层，不是固定版本的 Gradle 模板。从当前官方项目生成器
开始，确保游戏、Loader、Mapping、API 和构建插件版本互相兼容。

1. 生成 Java 21 / Minecraft 1.21+ Fabric 项目。
2. 把本示例的 `src` 目录复制进去。
3. 把 `sdk/java/src/main/java/io/github/sunrioa/rin` 复制到生成项目的
   `src/main/java/io/github/sunrioa/rin`。
4. 启动 Rin，并按需设置 `RIN_URL` / `RIN_TOKEN` 环境变量。
5. 启动服务器，以玩家身份输入 `/rin-npc ask`。

该命令创建隔离且启用 `outcome-reporting-v1` 的 Session，观察交互并提交
异步 Proposal Job。应用前会重新读取 Session：Proposal 必须仍是 `pending`，
而且 World Revision（非世界 Proposal 则为创建 Revision）必须仍然匹配。
过期 Proposal 不产生游戏效果，只报告 Rejected。允许的结果通过
`MinecraftServer.execute` 在服务器线程应用，并在实际 Accept/Reject 时
读取服务器 Tick。应把只发聊天的 `switch` 替换为自己的 NPC API；绝不能
让模型文本直接调用命令、发放 Item 或修改世界。

完整 Create Payload（包括 Request ID 与 Seed）在模糊失败后的重试中保持
不变。只有在 Rin 尚未产生任何在线 Proposal 的冷启动不可用场景，游戏才
执行一个明确编写的 Offline Fallback；一旦已有 Proposal，State 读取失败
必须 Fail Closed。Outbox 仍有待处理 Entry 时，不得开始新 Turn。Mod 还会
在提交前保留完整 Propose Request，并在收到 `202` 后立即保存 Job ID；未决
Attempt 会在下一次命令中用同一身份恢复，不增长 Sequence，也不选择
Fallback。只有游戏效果、Applied Marker 与 Outbox 在同一事务中落盘时才
移除 Attempt；任一种保留状态都会阻止新 Turn。

本源码示例只在内存中保存 Applied Operation 与 Outbox。每条 Commit 同时
保存一个只含 Memory 与绝对 Fact 的安全 Observe 降级载荷；临时错误保留
原 Commit，只有 `unknown_proposal` 等明确终态错误才原子转换为 Observe。
必须在 Durable ACK/Delete 成功后才能 Evict。生产接入应把所有标记 Hook
替换为可失败的权威世界/玩家数据事务，同时包住游戏效果、Applied Marker、
两份报告载荷、保留的 Create/Propose Request、可选 Job ID 及
Session/Sequence 状态。示例会在效果 Callback 抛错时移除 Marker/Outbox，
但只有真实游戏保存事务才能回滚已经部分写入的世界效果。

参考模板：https://github.com/FabricMC/fabric-example-mod

项目结构：https://docs.fabricmc.net/develop/getting-started/project-structure

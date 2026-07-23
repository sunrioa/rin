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

该命令创建隔离的示例 Session，观察交互，提交异步 Proposal Job，验证三个
Action ID 之一，再使用 `MinecraftServer.execute` 在服务器线程应用。只有
应用后才 Commit。应把只发聊天的 `switch` 替换为自己的 NPC API；不要让
模型文本直接调用命令、发放 Item 或修改世界。

参考模板：https://github.com/FabricMC/fabric-example-mod

项目结构：https://docs.fabricmc.net/develop/getting-started/project-structure

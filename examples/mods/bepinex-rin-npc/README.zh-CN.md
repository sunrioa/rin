# BepInEx Rin NPC 示例

[English](README.md) | [简体中文](README.zh-CN.md)

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
验证 `talk`、`wait` 或 `refuse`，在 Unity 主线程调用 `NpcActionReady`，
并且只在应用后 Commit。真实游戏专用插件应订阅该事件，把这些 ID 映射到
自己的 NPC API。

官方插件教程：https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/index.html

配置指南：https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/4_configuration.html

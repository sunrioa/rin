# Rin 示例

[English](README.md) | [简体中文](README.zh-CN.md)

请从 [`basic`](basic/) 开始。它刻意保持简短，只演示如何针对运行中的 Sidecar
创建 Session 并记录一次 Observe。其 ID 只存在于当前进程，因此它是开发 Smoke
Test，不是生产存档架构。

可安装的 Node.js 18+ [`terminal-story`](terminal-story/) 是覆盖
Windows/macOS/Linux 的可玩纵向切片，包含安全 JavaScript SDK 工作流、可复现
Sidecar 基准，以及不回避结果的持久化规则树对照。

各引擎与 Mod 目录演示宿主特有的线程和打包方式，并已持久化稳定的 Workflow
恢复状态，但仍属于 `advisory`。真实接入必须把效果 Apply 与 Operation Marker
连接到游戏自己的权威存档或幂等边界；声明生产稳定前应执行
[真实宿主验收矩阵](../docs/host-integration-validation.zh-CN.md)。

[`native-host`](native-host) 是面向原生引擎的无依赖 C99 参考；它在
GCC/Clang 与 MSVC 上运行共享 Host Scenario，不引入游戏引擎、JSON、HTTP
或 Shell 依赖。

[`mods/luanti-rin-npc`](mods/luanti-rin-npc) 是完整的 Loopback-only Luanti
服务端 Mod。官方 Luanti 5.16.1 Dedicated Server 已对同一世界各加载源码 Mod
和新生成脚手架两次；多人、实时 Sidecar、强制停止与 Soak 仍是人工门禁。

[`unreal/RinHost`](unreal/RinHost) 是 Preview Unreal Runtime Plugin 骨架，
覆盖显式存档/World Epoch 绑定、Game Thread 授权、类型化 Blueprint Capability
和 Behavior Tree ActionRun 回报。CI 检查跨平台布局与安全边界；Unreal Editor
构建仍是人工门禁。

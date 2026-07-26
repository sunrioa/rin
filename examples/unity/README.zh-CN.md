# Rin Unity 适配器

[English](README.md) | [简体中文](README.zh-CN.md)

本目录是可安装的 Unity Package Manager 包，并声明最低 API Level 为 Unity
`2021.3`。
从本地 Checkout 添加包，把 `RinClient` 与 `RinUnityWorkflow` 挂到一个跨场景
保留的 GameObject，关联 Client 字段，并把精简的 `RinNpcExample` 留在游戏代码。

`RinUnityWorkflow` 在
`Application.persistentDataPath/rin/default.json` 保存最多 1 MiB 状态，包括
稳定周目 Identity、完整 Proposal Attempt、Job ID、Tick High-water、已应用标记
与最多 64 条报告。Flush 临时文件后使用可恢复的 Target/Backup 双重 Rename，
不依赖 Windows 不可靠的覆盖式 Rename。

持久保证仍为 `advisory`：示例效果可在进程内回滚，但一般 Unity 世界/存档变更无法
与此 Sidecar 状态文件原子提交。生产游戏应让 Operation ID 对效果真正幂等，
或把该边界替换为游戏存档事务。

Token 在运行时从 `RIN_TOKEN` 读取，不会序列化进 Scene 或 Prefab；默认
Loopback HTTP 无需 Token。

`tools/verify_unity.py` 使用严格 Unity API Stub 编译该包，并在 .NET 6 执行
持久化/重启测试；CI 会在 Linux 与 Windows 运行。它是包与编译器验证，不是
Unity Editor 导入或已构建 Player 测试。声明兼容某个 Unity 版本或 Scripting
Backend 前，应执行
[真实宿主验收矩阵](../../docs/host-integration-validation.zh-CN.md)。

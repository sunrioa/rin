# Rin Unity 适配器

[English](README.md) | [简体中文](README.zh-CN.md)

本目录是可安装的 Unity Package Manager 包，并声明最低 API Level 为 Unity
`2021.3`。
从本地 Checkout 添加包，把 `RinClient` 与 `RinUnityWorkflow` 挂到一个跨场景
保留的 GameObject，关联 Client 字段，并把精简的 `RinNpcExample` 留在游戏代码。

`RinUnityWorkflow` 在
`Application.persistentDataPath/rin/default.json` 保存最多 1 MiB 状态，包括
稳定周目与可信内容 Binding、完整 Pending Turn、不透明的宿主编写参数、Active
Run、Tick High-water、Applied Marker 与最多 64 条精确报告。Flush 临时文件后
使用可恢复的 Target/Backup Replace，支持 Windows。State Schema 3 会直接拒绝
早期有损 Preview 格式，不会假装能恢复其中已退化成 `{}` 的动作参数。

每次 Managed Domain 生命周期都会提升 Host 与 Timeline Generation；Scene Load
会提升 World Generation。`RinUnityActionGate` 在替换 Authority 前取消活动动作；
不能证明取消时回报 `outcome-unknown`，后台完成会调度回 Unity Thread，旧
Generation 的迟到 Callback 会被忽略。Domain Reload 会把持久 Active Run 恢复为
`outcome-unknown`，不会盲目再次执行同一个 Operation。

本地校验与协议上限一致：Identifier 最多 96 个安全字符，所有 Wire Counter
最高为 `9007199254740991`。

`RinNpcExample` 演示游戏编写的 `movement.move_to` Offer。
`RinNavMeshAction` 拥有 `NavMeshAgent.SetDestination`，跨帧观察完成、取消时
Reset Path，并返回类型化终态。模型只选择密封 Offer，不能提供 Transform、
任意坐标、方法名或控制台命令；实际项目应替换为游戏拥有的 Destination 与
Capability Allowlist。

持久保证仍为 `advisory`：移动效果与此 Sidecar 状态文件不共享事务。生产游戏应
在权威存档中让 Operation ID 对效果真正幂等，或把该边界替换为游戏存档事务。

Token 在运行时从 `RIN_TOKEN` 读取，不会序列化进 Scene 或 Prefab；默认
Loopback HTTP 无需 Token。

`tools/verify_unity.py` 使用严格 Unity API Stub 编译该包，并在 .NET 6 覆盖
Binding、Scene/Domain Generation、取消、迟到 Callback、不透明参数、Active Run
恢复和文件替换；CI 会在 Linux 与 Windows 运行。它是包与编译器验证，不是
Unity Editor 导入或已构建 Player 测试。声明兼容某个 Unity 版本或 Scripting
Backend 前，应执行
[真实宿主验收矩阵](../../docs/host-integration-validation.zh-CN.md)。

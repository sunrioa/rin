# Rin Unreal Runtime Plugin 骨架

[English](README.md) | [简体中文](README.zh-CN.md)

这个 Preview Runtime Plugin 把 Rin 通用 Host 边界映射到 Unreal，但不会把
游戏权威移动到 Sidecar。

- `URinHostSubsystem` 遵循 `UGameInstanceSubsystem` 生命周期。
- `FWorldDelegates::OnPostWorldInitialization` 只为所属 Game Instance 推进
  World Epoch。
- Capability 注册与最终 Invocation 授权在 Game Thread 执行；
  `DispatchToGameThread` 是唯一跨线程入口。
- Blueprint 只看到类型化 Capability、Epoch、Invocation 与 ActionRun；不会
  获得 Shell、Console Command、Reflection 或任意函数执行入口。
- `UBTTask_RinHostMoveTo` 演示把一项长时间语义动作映射到原生 Behavior Tree
  移动和单调 ActionRun Callback。

加载时，游戏必须用稳定的存档/Profile ID 和持久化 Boot Generation 调用
`ConfigureHostIdentity`，再用权威 World/Timeline Generation 调用
`BindWorldIdentity`。Plugin 有意不从进程 GUID、对象指针、PIE 名称或地图路径
推断身份。本地边界将 Identifier 限制为 96 个安全字符，将 Epoch/Progress
Counter 限制为 `1..9007199254740991`；到达上限后递增会 fail closed。
发布 Decision Window 前，游戏先调用 `ObserveAuthoritativeClock`，再用
`ReplaceActionOffers` 发布当前完整的 Host 权威 Offer 集合。`OfferDigest` 必须是
完整 canonical Offer（包括本骨架保持 opaque 的 Arguments 与 Targets）的
SHA-256。选中的 Invocation 必须逐项重复 Offer 身份、Actor、Capability、Epoch、
Observation Sequence、Deadline 与 Digest，只额外增加 `OperationId`。

`AuthorizeAndQueueInvocation` 会消费一个当前 Offer，并在一次 Game Thread 调用中
完成最终 Epoch、完整 Offer Binding、权威 Deadline、精确 Capability
Version/Digest、撤销和重复 Operation 检查。Capability 被撤销或时钟越过 Deadline
时，对应的排队 Run 会立即变为 `stale`；`ReportRun(... Running ...)` 还会在
Behavior Tree 真正启动前再次检查。只有先完成授权入队，才可启动
`UBTTask_RinHostMoveTo`；该 Task 只负责为已入队 Operation 报告 `running` 和终态。

替换 World 会使已绑定 World Epoch 失效，并将未完成 Run 改为
`outcome-unknown`；只有权威存档加载完才能重新绑定。`ForkTimeline` 也会先使
未完成工作失效，再推进 Timeline Generation。每个进度 Callback 都携带启动时
捕获的 Epoch，因此已卸载 World 的迟到 Callback 无法恢复旧 Run。

把 `RinHost/` 复制到项目 `Plugins/` 下，重新生成工程文件，再使用该项目安装的
Unreal Engine 构建。仓库会在 Windows 与 Linux 执行静态结构/安全检查，但不
声称已经通过 Unreal Editor 或打包 Player 验证。

有界内存 Operation Set 与 Run Map 不会被宣传为持久实现。真实游戏必须把它们、
Pending Decision 和精确 Outbox 连接到权威 SaveGame/Database Transaction。
只有效果与 Operation Marker 一起提交才能声称 `idempotent-action`；效果、
Marker 与 Outbox 一起提交才能声称 `transactional-action`。

生命周期选择遵循 Epic 官方
[Programming Subsystems](https://dev.epicgames.com/documentation/en-us/unreal-engine/programming-subsystems-in-unreal-engine)
与 [Unreal Engine Modules](https://dev.epicgames.com/documentation/en-us/unreal-engine/unreal-engine-modules)
指南；World 初始化使用文档化的
[`FWorldDelegates`](https://dev.epicgames.com/documentation/en-us/unreal-engine/API/Runtime/Engine/FWorldDelegates)
Callback。

# 游戏 Adapter

[English](game-adapters.md) | [简体中文]

Adapter 把引擎生命周期事件转换成 Rin `0.7.0` **Preview** 协议对象，不会把游戏
权威交给 Rin。

## 最小 Host 循环

1. 网络工作前恢复稳定 Session 身份、Pending Turn、Applied Operation Marker
   与 Report Outbox。
2. 捕获一条权威 Observation、Epoch、Host Timepoint 和单调 Observation
   Sequence。
3. 创建 Decision Window 与 1–32 个完整绑定的 Action Offer。
4. 先持久化完整 Pending Turn，再提交或恢复 Proposal。
5. 将返回 Proposal 与持久 Session、Request、Actor、Window 和所选 Offer 匹配。
6. 在权威线程再次检查 Epoch、Deadline、Capability Digest、Target 与游戏规则。
7. 用稳定 Operation ID 执行，或拒绝。
8. 将确切 `ReportActionRequest` 保存到 Outcome Outbox，直到确认成功。

不得把传输错误或结果不明的 Job 当成执行第二个动作的许可。

## 线程

- Render、Input、Simulation 线程不得等待 HTTP 或模型完成。
- 在所属线程捕获引擎对象，但只持久化普通有界数据与不透明 `HostRef`。
- 只能在权威线程解析 `HostRef` 和执行 Capability。
- Thread、Task、Future、Socket、HTTP Object 与 Token 不得进入游戏存档。

## 持久化

复用 SDK Coordinator 负责 Workflow 顺序；游戏负责存储保证。应诚实选择
[Host Durability Profile](host-durability.zh-CN.md)。独立 JSON 文件通常只能
证明 `advisory`；只有游戏状态让 `operation_id` 可安全重试时才能声明
`idempotent-action`；只有效果、Marker 与 Outbox Entry 共用一个游戏事务时
才能声明 `transactional-action`。

State Reader 必须区分：

- `not_found`：可以初始化新身份；
- 有效状态：恢复；
- 不可读、格式错误、超限或不一致：Fail Closed。

Pending Turn、Marker、Outbox Entry 与状态文件字节数都必须有上限。文件 Flush
与 Replace 要跨平台，并在 Linux、Windows 测试中断替换。

## Ren'Py

复制 [`rin_client.py`](../adapters/renpy/rin_client.py)、
[`rin_epoch.py`](../adapters/renpy/rin_epoch.py) 与
[`rin_bridge.rpy`](../adapters/renpy/rin_bridge.rpy)。从后台 Worker 使用 Client，
只把普通 Dictionary 送回主线程；创建 Offer 前，用游戏提供的稳定存档与 World ID
调用 `rin_bind_host_epoch`。

Bridge 只把 `rin_host_epoch_state` 放入 Rollback/Save Store。有界、纯数据的
`persistent.rin_host_epoch_ledger` 在 Rollback 外保存 Host/Timeline 高水位，
Replica Merge 取最大 Generation。`after_load` 和每段 Rollback 的首次 Interaction
会 Fork Timeline，并使全部已登记结果失效，包括已经完成但尚未消费的结果。
首次调度和恢复都要求 Decision Window 与每个 Offer 精确匹配当前 Epoch；迟到
Worker 结果变成 `stale_epoch`。Bridge 在 Load 后调用
`renpy.block_rollback()`，因为 Ren'Py 官方指南要求 Callback Migration 不得再次
被 Rollback。

Registry 仍是进程内对象；持久剧情状态必须保存完整 Pending Turn 与返回的 Job ID。
Worker、Lock、HTTP Response 与 Token 都不得写入 Rollback/Save Data。
读取有界响应体时，Adapter 会沿用连接建立前启动的 Transport Deadline，
不会因每次 Socket Read 而重新计时。Python 测试
覆盖进程重启、旧存档 Load、重复 Rollback、Persistent Ledger Merge、上限、损坏
状态、Worker 失效与迟到完成。该 Suite 在 Linux CI 运行；本地 macOS 的
Ren'Py 8.5.3 Lint 和真引擎 Rollback Harness 已通过，Windows 执行尚未自动化。
真实项目仍须在准确 Ren'Py Build 中验证可见 Save/Load、Screen 更新、Shutdown
与 Sidecar Restart。

## Godot 4

[Godot Reference](../examples/godot/README.zh-CN.md) 使用 `HTTPRequest` Signal
和每 Save Slot 一个 `RinWorkflow`。它把有界 JSON 存在
`user://rin/<slot>.json`，网络前保存 Pending Turn 与 Outbox，而且 Transport
Client 不修改世界。

应从 Gameplay Event 或权威 Simulation Step 调用，不得在每帧 `_process()` 中
调用。用于持久世界修改前，应替换 Advisory File Store 或让执行真正幂等。

## Unity

[Unity UPM Package](../examples/unity/README.zh-CN.md) 包含：

- `RinClient`：有界、禁止 Redirect 的 `UnityWebRequest` Coroutine Transport；
- `RinUnityWorkflow`：Startup Recovery、Epoch、Pending Turn、Operation Marker
  与 Report Outbox；
- `IRinUnityHost`：游戏所有的 `CaptureTurn` 与幂等 `Execute` 边界。

`ActionOffer.arguments` 保持任意 JSON Object，不强制使用对白或某个引擎专用 DTO。
Harness 会在 Linux、Windows 编译 Package，并验证 Restart、Backup Recovery、
损坏状态、磁盘写入失败与原始 Argument 保留。这不能替代有 License 的 Unity
Editor/Player 测试。

## Unreal

Preview [Unreal Runtime Plugin](../examples/unreal/RinHost/README.zh-CN.md)
使用 `UGameInstanceSubsystem` 与限定所属 Game Instance 的 World Delegate。
游戏必须从权威存档注入稳定 Session、Host、World 与 Timeline Generation；
Adapter 不会从 PIE 或地图名称猜测它们。Capability 注册与
`AuthorizeAndQueueInvocation` 在 Game Thread 执行，Behavior Tree MoveTo
Task 演示单调的长动作回报。替换 World 或 Fork Timeline 会把未完成工作改为
`outcome-unknown`。

Linux 与 Windows CI 会静态拒绝不安全执行入口、大小写冲突和保留路径，但这不等于
Unreal Header Tool、编译器、Editor、打包 Player、SaveGame 事务或导航 Runtime
测试。

## Mod Host

Fabric、BepInEx Mono/IL2CPP 与 Luanti 示例展示 Server/Main Thread Dispatch 和
持久 SDK Workflow Store。它们是 Advisory Reference，不证明生成 NPC 已适配
每个游戏的存档与线程模型。用 [`rin init host`](host-scaffolding.zh-CN.md) 生成
固定起点后，还要完成[真实 Host 验收矩阵](host-integration-validation.zh-CN.md)。

## 引擎无关审查

- Offer 只包含捕获时已经合法的动作。
- 高权威效果留在游戏代码。
- 有引擎导航/物理 API 时优先使用；Vision Model 是可选 Observation 来源，不是
  默认移动系统。
- TTS 消费已经批准的对白；音频播放不改变动作权威。
- 长 Operation 回报 Queued/Running/Terminal 进度，并在引擎支持时使用协作取消。
- 根据 Session 预期寿命配置 Storage Metric、Retention、Snapshot/Export 与 Log。

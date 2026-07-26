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

从后台 Worker 使用
[`adapters/renpy/rin_client.py`](../adapters/renpy/rin_client.py)，只把普通
Dictionary 送回主线程。Registry 是进程内对象；持久剧情状态必须保存完整
Pending Turn 与返回的 Job ID。Worker、Lock、HTTP Response 与 Token 都不得写入
Rollback/Save Data。

Adapter 与 Bridge 测试可在 macOS、Linux、Windows 运行而无需启动 Ren'Py。
真实项目还必须在目标 Ren'Py Build 内验证 Save/Load、Rollback、Screen 更新、
Shutdown 与 Sidecar Restart。

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

## Mod Host

Fabric、BepInEx Mono/IL2CPP 与 Luanti 示例展示 Server/Main Thread Dispatch 和
持久 SDK Workflow Store。它们是 Advisory Reference，不证明生成 NPC 已适配
每个游戏的存档与线程模型。用 [`rin init mod`](mod-scaffolding.zh-CN.md) 生成
固定起点后，还要完成[真实 Host 验收矩阵](mod-integration-validation.zh-CN.md)。

## 引擎无关审查

- Offer 只包含捕获时已经合法的动作。
- 高权威效果留在游戏代码。
- 有引擎导航/物理 API 时优先使用；Vision Model 是可选 Observation 来源，不是
  默认移动系统。
- TTS 消费已经批准的对白；音频播放不改变动作权威。
- 长 Operation 回报 Queued/Running/Terminal 进度，并在引擎支持时使用协作取消。
- 根据 Session 预期寿命配置 Storage Metric、Retention、Snapshot/Export 与 Log。

# 真实宿主接入验收

[English](host-integration-validation.md) | [简体中文](host-integration-validation.zh-CN.md)

Rin `0.7.0` 是 Preview 软件。编译通过、模拟引擎 API 和面向重启的单元测试是有价值的
门禁，但不能证明 Mod 在真实游戏内稳定。下列对应行尚未形成实测证据前，引擎与 Mod
示例仍属于 `advisory`。

## 当前 CI 已证明的范围

| 接入 | 自动化证据 | 尚未证明 |
| --- | --- | --- |
| Fabric | 真实 Mod JAR/NBT 往返、官方 Dedicated Server GameTest 与 Authority Matrix | 实时 Sidecar 恢复、多人、强制停止和打包客户端 Integrated Server Smoke |
| BepInEx Mono/IL2CPP | 真实 BepInEx Package 编译与 Core 重启测试 | 代表性游戏中的 Plugin 加载、游戏 Hook、存档身份和关机流程 |
| Luanti | Lua 5.1/5.4 测试，以及 macOS/Windows 上官方 5.16.1 LuaJIT Dedicated Server 与真实 ModStorage 重启测试 | 实时 Sidecar、并发玩家、强制终止、地图保存时序与 Soak |
| Godot | Linux/Windows 官方 4.6.3 Headless Authority Generation、精确 Offer Binding、Active Run 恢复、重启与文件失败测试 | Editor Session 与 Export Build 中的实时 Sidecar 流量 |
| Unity | 严格 API Stub：Scene/Domain Generation、NavMesh 编译、取消、迟到 Callback、Active Run/不透明参数恢复与 Windows-safe Replace | Unity Editor Package 导入和 Mono/IL2CPP Player 构建 |
| Unreal | Runtime Plugin 结构、不安全入口与 Windows 路径测试 | Unreal Header Tool/编译器、Editor 加载、打包、SaveGame 与导航 Runtime |
| Ren'Py | Python Adapter/Epoch 测试；本机 Ren'Py 8.5.3 Lint 与 Rollback Harness | 可见引擎 Save/Load、Interaction Restart 与打包 Build |
| OpenSpiel | macOS/Linux/Windows 上真实 2.0.1 顺序/同时/Chance/隐藏信息游戏 | 仅作语义 Oracle；不含引擎线程、存档、Sidecar 或长世界动作生命周期 |
| Terminal Story | Windows、macOS、Linux 上真实 Sidecar 20 回合 CI | 它是参考游戏，不能证明另一款游戏的 Mod 生命周期 |

## 通用崩溃与恢复矩阵

仓库内 `rin.host-scenarios/v1` Contract 会索引以下场景的可执行证据：旧 Epoch
拒绝、稳定 Operation 幂等、动态 Capability 撤销、精确 Outbox Retry、长动作
Epoch Cancel、Authority Thread 非阻塞、恢复清理、同时决策、Host-owned Chance
与隐藏信息 Noninterference。一个 Scenario Entry 只证明列出的 Evidence File
及其 CI Runner，不表示每个引擎都已经通过所有场景；下列 Host-specific 缺口仍是
人工发布门禁。

所有适用场景都应针对真实存档的副本执行。在指定边界强制结束游戏或 Sidecar，不要用
单元测试中抛出的异常代替真实进程终止。

1. 已持久化 Pending Turn、尚未发送请求。
2. Sidecar 已接受请求，但响应丢失。
3. 持久化异步 Job ID 前后，包括轮询期间重启 Sidecar。
4. 已应用游戏效果、尚未持久化 Operation Marker 或 Outcome Outbox Entry。
   `advisory` 接入可以暴露这个重复窗口；晋级必须依靠游戏事务或可封闭窗口的幂等
   Operation Primitive。
5. 已发送 Outcome，但确认响应丢失。
6. 临时文件/备份替换期间，以及宿主正常自动存档期间。
7. Sidecar 缺席、延迟启动、重启，以及游戏正常关闭期间不可用。

每次重启后都要验证 Request/Event ID 保持稳定、Turn 不重叠、已经应用的 Operation
不会再次应用、未决工作仍可恢复，且 Outcome Outbox 最终排空。

## 各宿主门禁

### Fabric

- 官方 GameTest 会启动真实 Minecraft Dedicated Server，检查 Lifecycle Binding
  与 Server Thread Dispatch。单测覆盖 Integrated/Dedicated 分类、持久
  Generation 与旧 Epoch 拒绝；Metadata 必须保持 `environment: "*"`。
- 把构建的 JAR 安装到锁定版本的 Minecraft `1.21.1` Fabric Dedicated Server，
  验证启动、Command/Event Hook 和 Server Thread 访问。
- 使用两个不同世界，重开同一世界，执行 `save-all flush`、正常 `/stop`、强制终止，
  并使用至少两个并发玩家。
- 确认 Save/World Identity 不会跨世界共用，恢复状态随权威世界存档保存；除 Linux
  外还需在 Windows 执行。
- 为确定性玩法行为增加 Fabric GameTest，并保留真实 Server Smoke Test 覆盖生命周期
  和打包。每个目标 OS 都要 Quick-play 单人世界，并确认日志绑定 `integrated`
  Authority。

### BepInEx

- 将 BepInEx 6 视为 Bleeding-edge/未正式发布版本，并锁定精确 Runtime Build。
- Mono 必须在一款具名代表性游戏中加载 DLL，从真实存档/Profile 获取
  `SaveIdentity`，验证主线程效果应用、依赖解析、退出和重启。
- IL2CPP 必须在具体游戏完成首次 Interop 生成后重复验收。把示例 `ApplyDialogue`
  Delegate 替换为真实游戏 Hook，并测试 AOT/Native Backend；只对通用 Package
  编译通过不算完成。

### Luanti

- 真实 Luanti Headless Server（官方 5.16.1 Dedicated Server）已对同一真实
  World 各加载源码 Mod 与新生成的
  独立脚手架两次。测试使用真实 ModStorage userdata，在引擎 LuaJIT 内运行
  SDK/State Suite，确认 World Identity 不变、Host/Timeline Generation 前进；
  Windows CI 还会使用 SHA-256 固定的官方 ZIP 重复执行。
- 保持 `secure.http_mods` 配置，并跨地图保存周期、`/shutdown`、强制终止和
  World 重开验证真实 ModStorage；自动化的正常重启不能替代这些故障边界。
- 覆盖并发玩家、Sidecar 缓慢/不可用响应，以及 Windows 和 Linux 上的
  Loopback/Redirect Policy。

### Godot

- 在 Editor 和导出的 Windows/Linux Build 中运行真实 Scene，并连接实时 Sidecar。
- 验证 `user://` 持久化、Scene Reload、应用退出、网络分区、UI 响应性，以及触碰
  Node 的 Callback 回到主线程。

### Unity

- 在声明的最低 `2021.3` API Level，以及项目准备公开声称支持的每个 Unity 版本中，
  通过 Unity Package Manager 导入 Package。
- 构建并运行 Windows Mono 与 IL2CPP Player，验证 Scene/Domain Reload、
  `Application.persistentDataPath`、Stripping/AOT、Coroutine/主线程、应用退出和
  通用崩溃矩阵。
- 在寻路期间切换 Scene、在 Editor Reload Script，并销毁 Host；确认只产生一个
  `cancelled`/`outcome-unknown` 终态报告，原始参数不变，迟到 Callback 不再产生
  效果。超过宿主编写 Deadline 的路径也要重复验证。

### Unreal

- 将 `examples/unreal/RinHost` 复制到真实项目 `Plugins` 目录，用项目的准确 Unreal
  Engine 版本运行 Unreal Header Tool、编译、Editor 加载，并打包 Windows
  Development 与 Shipping Build。
- 从真实 SaveGame 恢复稳定 Session/Host/World/Timeline Generation；覆盖 PIE
  多实例、Server Travel、无缝/非无缝地图切换、存读档、正常关闭、强制终止和
  World 重开。
- 在权威 Game Thread 运行 Behavior Tree 移动示例；寻路期间取消、运行中卸载
  World，并确认迟到 Callback 不能恢复旧 Epoch 或重复 Operation。
- 声称 Idempotent/Transactional Durability 前，必须用 SaveGame/Database
  Transaction 替换有界内存 Marker。

### Ren'Py

- 在真实引擎中验证 Save/Load、Rollback、Interaction Restart 和正常关闭。
- 绑定由游戏拥有的稳定 Save/World ID，确认加载旧存档和 Rollback 后首次
  Interaction 都会把 Timeline 提升到 Persistent 高水位以上，并确认旧 Worker
  完成结果变成 `stale_epoch`。
- 确认序列化状态只包含普通恢复数据，不包含存活的 Worker、Socket、Lock 或
  Callback Object。

## Soak 与发布证据

建议把每个声称支持的宿主/Backend 至少两小时或 1,000 Turn 的运行作为 Preview
发布门禁，并注入 Timeout、Connection Reset、Sidecar Restart 和 Game Restart。
这是可重复的最低门禁，不是所有游戏必然稳定的证明。必须满足：

- Thread、Task、Handle、Memory、恢复文件和 Outbox 均无无界增长；
- 无重复世界效果，也无永久重叠 Turn；
- 每个 Accepted Turn 最终恢复，或得到明确 Terminal Error；
- 正常日志不含 Credential、完整玩家文本或存档 Payload；
- 在游戏自己的预算内没有不可接受的 Frame/Server Tick Stall。

记录 Rin Commit 与 Artifact Hash、准确的游戏/Loader/Engine/Backend 及版本、OS 与
文件系统、完整 Mod List、Save Identity 来源、测试/崩溃点、预期和实际结果、相关的
脱敏日志，以及剩余 Pending Turn/Attempt/Outbox 数量。

只有这些证据完成审查后才能提升宿主持久等级。只有同一个 Operation ID 重复执行且
游戏效果不重复，才可称为 `idempotent`；只有游戏效果、Operation Marker 与持久
Outcome 由同一个真实事务提交，才可称为 `transactional`。

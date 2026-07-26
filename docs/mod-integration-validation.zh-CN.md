# 真实宿主 Mod 接入验收

[English](mod-integration-validation.md) | [简体中文](mod-integration-validation.zh-CN.md)

Rin `0.7.0` 是 Preview 软件。编译通过、模拟引擎 API 和面向重启的单元测试是有价值的
门禁，但不能证明 Mod 在真实游戏内稳定。下列对应行尚未形成实测证据前，引擎与 Mod
示例仍属于 `advisory`。

## 当前 CI 已证明的范围

| 接入 | 自动化证据 | 尚未证明 |
| --- | --- | --- |
| Fabric | 真实 Mod JAR 构建与 NBT 恢复状态往返 | 在真实 Minecraft Dedicated Server 中加载和恢复 |
| BepInEx Mono/IL2CPP | 真实 BepInEx Package 编译与 Core 重启测试 | 代表性游戏中的 Plugin 加载、游戏 Hook、存档身份和关机流程 |
| Luanti | Lua 5.1/5.4 Workflow 测试与忠实模拟 ModStorage 的 Harness | 真实 Luanti Headless Server、世界保存和并发玩家 |
| Godot | 官方 Godot 4 Headless 解析和重启测试 | Editor Session 与 Export Build 中的实时 Sidecar 流量 |
| Unity | 严格 Unity API Stub 与 .NET 重启测试 | Unity Editor Package 导入和 Mono/IL2CPP Player 构建 |
| Ren'Py | Python Adapter 测试 | 引擎存档/读档、Rollback 与 Interaction Restart |
| Terminal Story | Windows、macOS、Linux 上真实 Sidecar 20 回合 CI | 它是参考游戏，不能证明另一款游戏的 Mod 生命周期 |

## 通用崩溃与恢复矩阵

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

- 把构建的 JAR 安装到锁定版本的 Minecraft `1.21.1` Fabric Dedicated Server，
  验证启动、Command/Event Hook 和 Server Thread 访问。
- 使用两个不同世界，重开同一世界，执行 `save-all flush`、正常 `/stop`、强制终止，
  并使用至少两个并发玩家。
- 确认 Save/World Identity 不会跨世界共用，恢复状态随权威世界存档保存；除 Linux
  外还需在 Windows 执行。
- 为确定性玩法行为增加 Fabric GameTest，并保留真实 Server Smoke Test 覆盖生命周期
  和打包。

### BepInEx

- 将 BepInEx 6 视为 Bleeding-edge/未正式发布版本，并锁定精确 Runtime Build。
- Mono 必须在一款具名代表性游戏中加载 DLL，从真实存档/Profile 获取
  `SaveIdentity`，验证主线程效果应用、依赖解析、退出和重启。
- IL2CPP 必须在具体游戏完成首次 Interop 生成后重复验收。把示例 `ApplyDialogue`
  Delegate 替换为真实游戏 Hook，并测试 AOT/Native Backend；只对通用 Package
  编译通过不算完成。

### Luanti

- 在真实 Luanti Headless Server 中加载 Mod 并配置 `secure.http_mods`。跨地图保存
  周期、`/shutdown`、强制终止和重开世界验证真实 ModStorage。
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

### Ren'Py

- 在真实引擎中验证 Save/Load、Rollback、Interaction Restart 和正常关闭。
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

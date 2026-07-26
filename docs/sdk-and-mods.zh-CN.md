# SDK 与 Mod 接入套件

[English](sdk-and-mods.md) | [简体中文](sdk-and-mods.zh-CN.md)

轻量接入套件把游戏自有适配器连接到 Rin，不会把世界权威移入 Sidecar 或
模型，同时消除重复的 HTTP、超时、Envelope 和 Job 轮询代码。

每个接入都必须声明真实的
[宿主持久 Profile](host-durability.zh-CN.md)。调用正确的 Rin Endpoint
不会自动让宿主具备 Crash Safety；更强 Profile 要求网络前持久边界，以及按
Operation ID 幂等的 Apply 或真实游戏事务。仓库中的宿主示例已经持久化稳定
Identity 与有界恢复状态，但其存储无法与任意游戏世界效果形成 Crash-atomic
边界，因此仍保持 `advisory`。

本文描述 Rin `0.7.0` Preview。权威 Wire Schema 是
[`api/openapi.json`](../api/openapi.json)。

## 支持矩阵

| 语言 | 最低运行时 | 调用模型 | JSON 边界 | 典型宿主 |
| --- | --- | --- | --- | --- |
| Python | 3.9 | 同步 | 标准库 | Ren'Py、工具、服务器 |
| JavaScript | Node 18 / Fetch 宿主 | Promise | 内置 | Electron、Web Bridge、Node |
| C# | .NET Standard 2.0 / .NET 6 | Task | `System.Text.Json` | BepInEx 6 Mono / IL2CPP、现代 .NET 游戏 |
| Java | 17 | `CompletableFuture` | 注入 `JsonCodec` | Fabric、JVM 服务器 |
| Lua | 5.1 | Callback | 注入 Codec 和 Transport | Luanti、嵌入式 Lua 引擎 |

每套实现暴露由 OpenAPI 生成到
[`sdk/conformance/routes.json`](../sdk/conformance/routes.json) 的 20 Route
Inventory。该清单只核对覆盖，不是第二份 Wire Contract，也不是行为证明。
Python 和 JavaScript 没有运行时依赖；C# 在 .NET 6 使用 Framework API，
其 .NET Standard 2.0 兼容 Target 固定 `System.Text.Json`；
Java 通过两个方法的 Codec 复用宿主 JSON 库；Lua 注入全部宿主服务，因为
不同 Lua 引擎的 HTTP 和 JSON API 不兼容。

## 目录约定

```text
sdk/
  conformance/       与语言无关的路由清单
  <language>/        源码、语言 README、测试、可选快速开始
examples/mods/
  fabric-rin-npc/    固定版本、可构建的 Fabric 服务端 Mod
  bepinex-rin-npc/   固定版本的 BepInEx 6 Mono 与 IL2CPP 项目
  luanti-rin-npc/    内置 Lua SDK 的完整服务器 Mod
```

SDK 采用源码优先分发，尚未发布到语言 Registry。应从同一个精确 Rin 仓库
Revision 或已验证 Release Tag Vendor 完整 Client 目录。不要只复制单个 Client
文件而遗漏 README 和生成的 Conformance Inventory。

新 Mod 应优先使用离线生成器，让宿主工程、完整 SDK、许可证声明、固定依赖与
SHA-256 清单保持同步：

```bash
rin init host --list-hosts
rin init host --engine fabric --id guide_npc \
  --name "Guide NPC" --namespace io.github.example
```

完整命令契约、Windows 路径规则与构建命令见
[通用 Host 脚手架指南](host-scaffolding.zh-CN.md)。生成和编译不能代替
[真实宿主验收矩阵](host-integration-validation.zh-CN.md)。

## 接入生命周期

1. 捕获一个有界、由游戏拥有的事件并调用 `observe`。
2. 创建 Epoch、Decision Window，以及游戏可安全实现的完整绑定 Offer。
3. 实时游戏使用异步 Proposal Job API。
4. 将返回 Offer 与持久化的 Host-authored Request 匹配。
5. 切回引擎拥有的线程并应用动作。
6. 在应用事务中把实际结果写入游戏自己的 Outcome Outbox。
7. 从 Outbox 调用 `reportAction`，必要时回报拒绝；失败只重报，不重复应用动作。

Proposal 提交、轮询、超时或取消若结果不确定，应标为 outcome-unknown 并
fail closed；使用相同身份恢复，Transport 层不得自行编造替代动作。提交前应
持久化完整 Propose Request 与 Operation 身份，并在 `202` 后立即保存 Job ID。
任何新 Turn 之前都要先恢复这条记录；只有游戏结果、Applied Marker 与 Outcome
Outbox 在同一个权威事务中落盘时才能清除。

JavaScript/TypeScript 与 C# SDK 保留底层的
`ProposalAttemptCoordinator`、可插拔 `OutcomeOutbox` 以及不透明 Snapshot
持久化 Helper；JavaScript/TypeScript、C# 与 Java 还提供更高层的
`WorkflowCoordinator`，在应用动作前校验声明的宿主持久 Profile。它们只定义
存储契约，不提供容易误用于生产的内存默认实现。
Transactional Settlement Hook 必须原子应用游戏效果、持久化 Applied Marker
与完整 Action Report，并删除 Pending Turn。幂等宿主会先收到稳定 Operation ID，再由
Store 完成 Report 事务。Outbox Drain 只确认普通成功或 Rin 明确返回的
exact-duplicate 成功。所有错误都保留原 Action Report Entry，Report 永远不会
转换为 Observation。`ProposalFreshness` 统一负责应用前的 Pending/Revision 校验。

Rin 内部的 Provider 失败可以在 Proposal 产生前使用 Deterministic Policy。
Sidecar Submit/Poll/Cancel 结果未决是另一种状态，不能转换成宿主动作。

不要从渲染或 Update 循环调用在线 Proposal 或 Generation 端点。一次玩家
交互最多启动一个 Job；普通帧只应检查本地 Future、Coroutine、Timer 或
主线程队列。

Action Report 是结果记账而不是执行授权。Outbox、延迟结果、相同 `request_id` 重试
和离线对账规则见[动作结果记账](action-lifecycle.zh-CN.md)。

所有公共 JSON 整数都必须处于 `-9007199254740991` 至 `9007199254740991` 的精确
跨语言范围。每个 Report 和 Batch Item 都必须显式序列化 `accepted`，包括
`false`。SDK 必须以 UTF-8 编码请求正文，在 Transport 前拒绝本地不安全整数，
拒绝非 JSON 本地值，并容忍增量 Response Member。由 OpenAPI 驱动的服务端仍是
Request Schema 的权威，会拒绝封闭 Request Object 中的未知 Member；调用方不得
把 SDK 的通用 Map/Object 边界当作完整的本地 Schema 校验。SDK 还必须区分非
2xx Rin Error Envelope 与携带 `data.error` 的 HTTP-200 终态 Job。

## 凭据与传输

- 模型供应商凭据只保留在 Rin Sidecar。
- 游戏可以持有用于向 Rin 鉴权的 `RIN_TOKEN`；它不是供应商 API Key，
  不能写入存档、日志或 Mod 配置。
- SDK 只对 loopback 接受明文 HTTP。远程 Rin Origin 必须使用 HTTPS 和
  Token。
- SDK 拒绝重定向、限制响应大小，并只向用户显示有界 Rin 错误码，不暴露
  供应商正文。
- 随附客户端默认响应上限为 32 MiB。完整 inline Snapshot compact JSON 上限
  为 16 MiB，超限返回 `413 snapshot_too_large` 且绝不截断。JavaScript 与 C#
  提供完整大 lineage 迁移的有界 Session Transfer stream；其他 package 仍是
  JSON transport client。
- Restore 调用方必须从运行中的可信内容 manifest 取得必填
  `expected_binding`，不能从导入 Snapshot 读取。
- Snapshot 是按事件日志保护的可信、不透明状态；其 SHA-256 canonical checksum
  只能发现意外损坏，既不认证来源，也不能阻止能重算 checksum 的一方。
- 优先 SDK 的不透明 Snapshot helper 保存完整有界 JSON Object，不经由可能丢失
  字段的强类型 State 投影，因此新增 Member 能跨 Save/Load/Restore 周期保留。
- 把生成对白当作显示数据。绝不能把它解析成控制台命令、反射目标、脚本名、
  Item ID 或文件路径。

Luanti 是有文档记录的例外：其引擎 HTTP 实现最多跟随三次重定向，Mod API
没有单请求关闭开关。因此示例只允许 loopback，并拒绝 Authorization
Header。要从 Luanti 支持经过鉴权的远程 Rin，应先使用更严格的原生 Bridge。

## 示例 Mod

Fabric 参考是固定 Minecraft 1.21.1 与 Java 21 的完整 Gradle 项目；其 JAR
内含源码优先的 Java SDK，通过 Saved Data 保存稳定世界/Session 身份和有上限
的流程状态，并用 `MinecraftServer.execute` 调度宿主工作。由于
`markDirty()` 是最终存盘而非持久事务边界，它仍保持 `advisory`。

BepInEx 参考固定上游尚未正式发布的 Bleeding-edge BepInEx 6
`6.0.0-be.785`，并把 Unity Mono
（`netstandard2.0`）与 IL2CPP（`net6.0`）分开。共享 Core 持久化稳定
Save Identity、Pending Turn、Job ID 与有上限的 Outcome Outbox。Mono
提供有上限的 Unity 主线程队列和可选 F8 演示；IL2CPP 明确要求游戏专用
Interop Hook，因为生成的 Assembly 不能跨游戏复用。
`python tools/package_bepinex.py` 会生成确定性、Windows-safe 的安装 ZIP，
并拒绝意外包含 BepInEx、Unity 或游戏 Interop Assembly 的包。
F8 切片还演示了游戏拥有的两阶段 Beacon Quest：Stage 与 Operation Marker
跨重启保留，无效转换会被拒绝，后续 Observation 也会携带 Stage，让记忆影响
下一次规划。

Luanti 示例是完整服务器 Mod。它只在模块作用域调用
`core.request_http_api()`，把返回 API 保持为 local，并要求
`secure.http_mods = rin_npc_example`。其 ModStorage Adapter 会跨重启保留
稳定 World/Session Identity、完整 Pending Turn、Job ID、单调 Tick 下限与
有界 Outcome Outbox。Lua SDK Workflow 负责 Submit/Poll Recovery、Identity
检查、Freshness、精确 Report 重试及 ACK 后 Evict。由于 ModStorage 保存时机与
任意游戏效果不能组成同步事务，其 Profile 仍为 `advisory`。

Godot 4.7.1 参考是可直接运行的项目。可复用 Workflow 在 `user://` 保存稳定
Save Slot Identity、完整 Pending Turn、Job ID、Tick High-water 与有界
Outcome Outbox；少于 250 行的 NPC 宿主只保留游戏拥有的 Policy 与效果。CI 对官方
Godot Binary 固定 SHA-512，并在 Linux 与 Windows 执行 Headless 解析和重启
测试。

Unity 包声明最低 API Level 为 `2021.3`。Coroutine Workflow 在
`Application.persistentDataPath` 维护可重启的 Pending Turn、Job、Freshness、
Settlement 与 Outbox 状态；游戏侧示例只有 18 行。.NET Harness 会在 Linux
与 Windows 使用 Unity API Stub 编译包并测试文件恢复。在执行
[真实宿主验收矩阵](host-integration-validation.zh-CN.md)前，这不能证明包已在
2021.3 或后续 Unity 版本中完成 Editor 导入或 Player 兼容测试。

## 验证

```bash
make test
make test-sdks
python3 tools/generate_contract.py --check
```

CI 执行 Go Format、Vet、Race Test，以及 Linux、macOS、Windows 上的 Zero-CGO
Build 和 File Store/Sidecar 生命周期测试；这些平台测试覆盖持久化、重启与第二
写者拒绝。CI 还会在 Python 3.9 与当前 Python 3 上运行 Python SDK/Ren'Py，在
Node 18/24、Java 17/25、.NET 6/10 上运行相应 Client Test，并在 Lua 5.1/5.4
下运行 Lua Client Test 与 Luanti 重启/写失败状态 Harness。固定版本的
Fabric 项目及其 Saved Data 重启测试，以及两个 BepInEx Backend、重启测试
与安装包都会在 Linux 和 Windows 构建。
Contract Generator Check 防止 OpenAPI 与生成的
Route/Version Projection 漂移。

SDK Test 会通过本地 Fake Transport 或 HTTP Test Server 真实调用 Client Method，
并断言 Method/Path、非空 UTF-8 JSON Body、Bearer/User-Agent Header、成功
Envelope Data 与 API Status/Code/Field Mapping；它们不是针对运行中 Sidecar
的 End-to-end Test。生成的 Route Manifest 会与 `httpapi.ContractRoutes()` 比较
以发现 Route 漂移；其余 Go Source Marker Check 只是静态防回退 Lint。Marker
或 Method Name 存在不能证明 Runtime Transport 行为。

## 主要参考

- [Fabric 示例 Mod（CC0）](https://github.com/FabricMC/fabric-example-mod)
- [Fabric 项目结构](https://docs.fabricmc.net/develop/getting-started/project-structure)
- [BepInEx 插件教程](https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/index.html)
- [BepInEx 配置](https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/4_configuration.html)
- [Java 17 HttpClient](https://docs.oracle.com/en/java/javase/17/docs/api/java.net.http/java/net/http/HttpClient.html)
- [.NET HttpClient JSON 扩展](https://learn.microsoft.com/en-us/dotnet/api/system.net.http.json)
- [`System.Text.Json` 支持的类型](https://learn.microsoft.com/en-us/dotnet/standard/serialization/system-text-json/supported-types)
- [Luanti HTTP API](https://docs.luanti.org/for-creators/api/http-api/)
- [Luanti Lua API 源码](https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md)

这些示例为 Rin 独立编写，没有复制上述项目的实现代码。链接用于说明宿主
生命周期、元数据和传输 API。Rin SDK、示例与文档按
[MIT License](../LICENSE) 发布。

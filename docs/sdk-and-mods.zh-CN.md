# SDK 与 Mod 接入套件

[English](sdk-and-mods.md) | [简体中文](sdk-and-mods.zh-CN.md)

轻量接入套件把游戏自有适配器连接到 Rin，不会把世界权威移入 Sidecar 或
模型，同时消除重复的 HTTP、超时、Envelope 和 Job 轮询代码。

## 支持矩阵

| 语言 | 最低运行时 | 调用模型 | JSON 边界 | 典型宿主 |
| --- | --- | --- | --- | --- |
| Python | 3.9 | 同步 | 标准库 | Ren'Py、工具、服务器 |
| JavaScript | Node 18 / Fetch 宿主 | Promise | 内置 | Electron、Web Bridge、Node |
| C# | .NET 6 | Task | `System.Text.Json` | BepInEx 6、现代 .NET 游戏 |
| Java | 17 | `CompletableFuture` | 注入 `JsonCodec` | Fabric、JVM 服务器 |
| Lua | 5.1 | Callback | 注入 Codec 和 Transport | Luanti、嵌入式 Lua 引擎 |

每套实现覆盖
[`sdk/conformance/routes.json`](../sdk/conformance/routes.json) 中的 20 条
路由。Python 和 JavaScript 没有运行时依赖；C# 只使用 Framework API；
Java 通过两个方法的 Codec 复用宿主 JSON 库；Lua 注入全部宿主服务，因为
不同 Lua 引擎的 HTTP 和 JSON API 不兼容。

## 目录约定

```text
sdk/
  conformance/       与语言无关的路由清单
  <language>/        源码、语言 README、测试、可选快速开始
examples/mods/
  fabric-rin-npc/    官方 Fabric 模板的源码覆盖层
  bepinex-rin-npc/   BepInEx 6 源码覆盖层
  luanti-rin-npc/    内置 Lua SDK 的完整服务器 Mod
```

SDK 当前以源码为主，尚未发布到语言注册表。应固定到带 Tag 的 Rin revision，
或直接引用源码项目。不要只复制单个客户端文件而遗漏 README 和 Conformance
版本。

## 接入生命周期

以下“先应用、再回报”步骤要求创建 Session 时请求
`outcome-reporting-v1`；否则 Runtime 会有意保留旧版 Commit 与重放行为。

1. 捕获一个有界、由游戏拥有的事件并调用 `observe`。
2. 只向 Rin 提供游戏能够安全实现的候选动作。
3. 实时游戏使用异步 Proposal Job API。
4. 用本地白名单验证返回的 Action ID 和 Payload。
5. 切回引擎拥有的线程并应用动作。
6. 在应用事务中把实际结果写入游戏自己的 Outcome Outbox。
7. 从 Outbox 调用 `commit`，必要时回报拒绝；失败只重报，不重复应用动作。
8. Rin 不可用时保留 authored 或 deterministic fallback。

Proposal 提交、轮询、超时或取消若结果不确定，应标为 outcome-unknown 并
fail closed；使用相同身份恢复，确认不存在在线 Proposal 前不得执行 fallback。
提交前应持久化完整 Propose Request 与 Operation 身份，并在 `202` 后立即
保存 Job ID。任何新 Turn 或 Fallback 之前都要先恢复这条记录；只有游戏结果、
Applied Marker 与 Outcome Outbox 在同一个权威事务中落盘时才能清除。

不要从渲染或 Update 循环调用在线 Proposal 或 Generation 端点。一次玩家
交互最多启动一个 Job；普通帧只应检查本地 Future、Coroutine、Timer 或
主线程队列。

Commit 是结果记账而不是执行授权。Outbox、延迟结果、相同 `request_id` 重试
和离线对账规则见[动作结果记账](outcome-reporting.zh-CN.md)。

## 凭据与传输

- 模型供应商凭据只保留在 Rin Sidecar。
- 游戏可以持有用于向 Rin 鉴权的 `RIN_TOKEN`；它不是供应商 API Key，
  不能写入存档、日志或 Mod 配置。
- SDK 只对 loopback 接受明文 HTTP。远程 Rin Origin 必须使用 HTTPS 和
  Token。
- SDK 拒绝重定向、限制响应大小，并只向用户显示有界 Rin 错误码，不暴露
  供应商正文。
- 随附客户端默认响应上限为 32 MiB。完整 inline Snapshot compact JSON 上限
  为 16 MiB，超限返回 `413 snapshot_too_large` 且绝不截断。当前不提供流式
  Snapshot 传输。
- Restore 调用方必须从运行中的可信内容 manifest 取得必填
  `expected_binding`，不能从导入 Snapshot 读取。
- Snapshot 是按事件日志保护的可信、不透明状态；其 SHA-256 canonical checksum
  只能发现意外损坏，既不认证来源，也不能阻止能重算 checksum 的一方。
- 把生成对白当作显示数据。绝不能把它解析成控制台命令、反射目标、脚本名、
  Item ID 或文件路径。

Luanti 是有文档记录的例外：其引擎 HTTP 实现最多跟随三次重定向，Mod API
没有单请求关闭开关。因此示例只允许 loopback，并拒绝 Authorization
Header。要从 Luanti 支持经过鉴权的远程 Rin，应先使用更严格的原生 Bridge。

## 示例 Mod

Fabric 覆盖层遵循官方项目布局，复用 Minecraft 的 Gson，并通过
`MinecraftServer.execute` 安排效果。应从当前 Fabric 模板生成构建文件，
不要在 Rin 中固定会老化的 Loom/Minecraft 组合。

BepInEx 覆盖层面向 BepInEx 6 和 .NET 6。它不会每帧发送 HTTP：
`Update` 只排空有上限的队列并可选检测 F8 演示按键。订阅
`NpcActionReady`，再通过目标游戏支持的 API 转换三个示例 ID。

Luanti 示例是完整服务器 Mod。它只在模块作用域调用
`core.request_http_api()`，把返回 API 保持为 local，并要求
`secure.http_mods = rin_npc_example`。

## 验证

```bash
make test
make test-sdks
```

主 Go 兼容套件检查路由覆盖、安全标记、引擎线程切换、本地动作白名单和
Luanti 内置客户端的精确同步。CI 运行 Python、JavaScript、Java、C# 以及
Lua 5.1 和 5.4；其他 Job 使用各 SDK 的最低受支持运行时。

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

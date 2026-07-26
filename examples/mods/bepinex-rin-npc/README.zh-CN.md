# BepInEx Rin NPC 参考接入

[English](README.md) | [简体中文](README.zh-CN.md)

这是一个可真实构建的参考项目，面向上游尚未正式发布的 Bleeding-edge
BepInEx 6，并按 Unity Backend 分开。

**Host durability profile：`advisory`。** 示例会持久化稳定身份、Pending
Turn、Job ID 与 Outcome Outbox，但通用 BepInEx 插件无法把任意游戏的存档
修改与该状态文件合并为同一个原子事务。参见
[宿主持久保证分级](../../../docs/host-durability.zh-CN.md)。

## 只选择一个 Backend

| Backend | 项目 | Target | 包含内容 |
| --- | --- | --- | --- |
| Unity Mono | `RinNpc.Mono` | `netstandard2.0` | 可加载插件、F8 演示、Unity 主线程队列 |
| Unity IL2CPP | `RinNpc.IL2CPP` | `net6.0` | 可加载 Transport 插件；必须提供游戏专用 Hook |

两个项目都固定 BepInEx `6.0.0-be.785`，不能同时安装。IL2CPP Interop
Assembly 由具体游戏生成，因此仓库不会假装一个通用 `UnityEngine` 引用足够。
真实 IL2CPP Adapter 必须把 `Plugin.ApplyDialogue` 设置为能切回游戏所有者
线程的 Delegate，并从交互 Hook 调用 `RequestNpcTurnAsync`。编译成功不能
证明任一 Backend 能在具体游戏中加载；还需执行
[真实宿主验收矩阵](../../../docs/mod-integration-validation.zh-CN.md)。

## 构建与安装

使用 .NET 6 SDK：

```bash
dotnet restore RinNpc.Mono/RinNpc.Mono.csproj --locked-mode
dotnet build RinNpc.Mono/RinNpc.Mono.csproj -c Release --no-restore

dotnet restore RinNpc.IL2CPP/RinNpc.IL2CPP.csproj --locked-mode
dotnet build RinNpc.IL2CPP/RinNpc.IL2CPP.csproj -c Release --no-restore
```

从本目录在 Linux、macOS 或 Windows 生成并独立复验确定性安装 ZIP：

```bash
python package_bepinex.py
python package_bepinex.py --verify-archive dist/rin-npc-bepinex-mono-0.6.0.zip
python package_bepinex.py --verify-archive dist/rin-npc-bepinex-il2cpp-0.6.0.zip
```

仓库根目录的 `python tools/package_bepinex.py` 命令会委托给同一个规范 Helper。

把正确 Backend 的 ZIP 解压到游戏根目录，它会生成
`BepInEx/plugins/RinNpc`。Mono 包含旧 Unity Mono 所需的
`System.Text.Json` Runtime 依赖；IL2CPP 包使用其 .NET 6 Runtime。每个包都会
拒绝对应受审查 Allowlist 之外的全部 DLL，包括 BepInEx、Unity 或游戏专用
Interop Assembly；扩展 Allowlist 前必须审查 Runtime File 与再分发 Notice。
两者都会包含
`LICENSE-RIN.txt`；Mono ZIP 还会包含并复验 `third-party/` 中经过审查的 .NET
许可与 Notice，IL2CPP ZIP 则有意省略该 Mono 专用集合。`manifest.json` 会记录每个安装文件的 SHA-256
Checksum。

首次启动后配置：

- `Connection.BaseUrl`：默认连接本机 Rin。
- `Identity.ProductIdentity`：每个游戏稳定不变，不能使用可执行文件路径。
- `Identity.SaveIdentity`：每个存档/Profile 稳定不变；生产接入必须替换
  Demo 值。
- `Example.EnableF8Demo`：仅 Mono 的隔离演示。

远程 Rin Bearer Token 只能通过 `RIN_TOKEN` 提供，不能写入 BepInEx Config
或游戏存档；远程 Origin 必须使用 HTTPS。

## 恢复与权威边界

`RinNpc.Core` 统一负责 Create/Observe、Proposal Job 恢复、Freshness、
Allowlist、Outcome 构造与 Outbox Drain。Backend Wrapper 只负责生命周期、
配置、Tick、日志和主线程 Apply。当前 Mono Wrapper 为 107 行、IL2CPP
Wrapper 为 89 行，共享 Runtime 为 247 行；更大的状态 Store 文件属于持久化
基础设施，不是复制到每个游戏的 Workflow。状态文件最大 2 MB，Outbox 最多 32 条；
文件名由 SHA-256 派生，可安全用于 Windows，位置为
`BepInEx/config/rin-npc-example`。

F8 纵向切片虽然很小，但已不再只有对白。Rin 可以提出游戏编写的 Beacon Quest，
之后再推进它；Game Store 拥有 `0 -> 1 -> 2` 转换，并在 Settlement 前保存
Operation ID，因此崩溃重试不会重复推进。当前 Quest Stage 会进入下一条
Observation，让持久效果对后续记忆与规划可见。阶段不符、过期或未列入 Allowlist
的动作会被拒绝，Authored Offline Fallback 仍固定为 `wait`。

网络提交前先保存完整 Pending Turn，收到 `202` 后立即保存 Job ID；重启会
恢复同一 Operation 与稳定 Session。动作应用后保存精确 Commit 及安全的绝对
事实 Observe Fallback。临时错误保留 Commit；只有 `unknown_proposal` 等明确
终态错误会先持久化转换，再发送 Observe。

文件替换只能保证抗崩溃的执行顺序，不代表游戏事务已经持久化。进程或断电仍
可能发生在游戏效果与状态文件替换之间。生产 Adapter 应让效果按 Operation
ID 幂等，或实现游戏专用事务 Store 后才能声明更强 Profile。不要让两个插件
实例同时操作同一状态文件。

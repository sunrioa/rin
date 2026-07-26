# Mod 脚手架

[English](mod-scaffolding.md) | [简体中文](mod-scaffolding.zh-CN.md)

`rin init mod` 会为一个已支持的游戏宿主创建自包含起始项目。它消除 SDK
Vendoring 与 Manifest 接线等机械工作，但不会猜测游戏专属的存档、线程或世界
修改 API。Rin `0.6.0` 与生成项目均属于 Preview 软件，每个生成接入都从
`advisory` 宿主持久保证分级开始。

生成、编译和真实游戏稳定性是三层不同的证据。命令成功只证明输入与内嵌模板通过
本地校验；构建成功只证明生成源码能够针对固定的宿主依赖编译。两者都不能证明 Mod
在某一具体游戏中稳定；作出该声明前必须执行
[真实宿主验收矩阵](mod-integration-validation.zh-CN.md)。

## 命令契约

列出当前 Rin Binary 内嵌的模板：

```bash
rin init mod --list-hosts
```

生成项目：

```text
rin init mod \
  --host fabric|bepinex-mono|bepinex-il2cpp|luanti \
  --id <mod_id> \
  [--name <display name>] \
  [--namespace io.github.user] \
  [--author <author>] \
  [--version 0.1.0] \
  [--output relative/path] \
  [--dry-run]
```

`--host` 与 `--id` 为必填项；`--name` 默认等于 Mod ID，`--version`
默认是 `0.1.0`，`--output` 默认是 `./<mod_id>`。Fabric 与两个 BepInEx
Backend 必须提供 `--namespace`；Luanti 模板没有全局 Owner Namespace 字段，
因此会拒绝该参数。Fabric 把它作为 Java Package Prefix；BepInEx 把它作为全局
唯一 Plugin GUID Prefix，而 C# Namespace 从 `--id` 派生。`--author` 可省略，
并且绝不会从操作系统、Git 配置或仓库元数据推断。Luanti 的 `--author` 会写入
`mod.conf`，因此若提供就必须是 ContentDB 用户名：1–64 个 ASCII 字母、数字、
下划线或连字符。

`--dry-run` 会执行相同的校验与渲染，输出确定性的文件和 SHA-256 清单，但不写入
任何内容。向已有游戏仓库接入时，推荐先运行这一命令。

四个精确 Host ID 为 `fabric`、`bepinex-mono`、`bepinex-il2cpp` 与
`luanti`；名称区分大小写，也不会接受含糊的 `bepinex` Alias。

### 标识符规则

- `--id` 长度必须为 2–64，并匹配
  `[a-z][a-z0-9]*(?:_[a-z0-9]+)*`。这一刻意收窄的语法可跨所有已支持模板
  移植；它比还允许连字符的 Fabric 接受语法更严格。
- `--name` 是玩家可见的显示名，必须是有效 UTF-8、非空且不含 NUL、换行或
  控制字符。
- `--namespace` 是类似 `io.github.example` 的小写点分反向域名 Owner
  Namespace。空 Segment、语言关键字、路径分隔符和 Windows 设备名 Segment
  会被拒绝。
- `--version` 必须使用数字 `major.minor.patch` 形式，总长最多 17 个 ASCII
  字符，且每个 Component 都必须处于 `0` 到 `65534`。它是生成 Mod 的版本，
  不是 Rin 版本。
- `--output` 必须是当前目录下的相对路径。绝对路径、`.`/`..` 穿越、替代
  Windows 分隔符、盘符或 UNC Path，以及当前目录以下为符号链接的 Output
  Ancestor 都会被拒绝。它的父目录必须已经存在。

这些限制在所有操作系统上生效，而不只是在 Windows。每个 Path Component 都按
大小写不敏感方式检查，并排除 `CON`、`PRN`、`AUX`、`NUL`、`COM1` 到
`COM9`、`LPT1` 到 `LPT9` 等 Windows 设备名。包含 Windows 保留字符、尾随
空格或尾随句点的 Component 同样会被拒绝，因此在 Linux 或 macOS 生成的项目
可以无需重命名便迁移到 Windows。生成器还要求最深的生成文件绝对路径不超过
240 个 UTF-16 Code Unit。Windows 上建议在 OneDrive 等同步目录之外使用较短的
ASCII 路径，例如优先使用 `C:\src`，不要放在层级很深的桌面或文档目录。

## 安全且确定性的输出

生成器离线工作：它只读取当前 `rin` Binary 内嵌的模板与 SDK 源码，不下载更新
模板、不检查无关 Git Checkout、不读取凭据，也不执行宿主构建工具。给定同一 Rin
Binary 与参数，无论时间、当前用户名或目标目录如何，生成的相对路径、UTF-8 文件
字节与排序后的 SHA-256 Manifest 均一致。

每个项目都包含对应宿主所需的完整 Source-first SDK，以及记录除 Manifest
自身之外全部生成文件、来源 Rin Release 与各自 SHA-256 Digest 的 Hash
Manifest。生成项目不得依赖
`../../../sdk` 一类路径；移出 Rin 仓库后仍须能够构建。Vendored SDK 会保留
Rin 的 MIT License Notice；生成器不会替游戏作者自己的 Mod 选择 License。
Fabric 脚手架还会以 `LICENSE-GRADLE.txt` 和 `NOTICE-GRADLE.txt` 携带所分发
Gradle Wrapper 对应的 Gradle 8.14.3 精确许可与 Notice。BepInEx Mono
脚手架只为安装 ZIP 实际再分发的 8 个 .NET Runtime DLL 携带经过审查、Hash
固定的许可与 Notice；IL2CPP 不分发这些 DLL，也不会声称适用 Mono Notice。

目标路径即使是空目录或符号链接，也不得已存在。设计上不提供覆盖或 Force 模式。
生成器还会拒绝大小写不敏感的同级冲突以及当前目录以下目标祖先中的符号链接。
如果创建目标后生成失败，部分目录树会原样保留，通常还会保留
`.rin-scaffold.incomplete`；不得构建或安装该目录。重试前请人工审查并删除或
移动它。生成器不会执行任何基于路径的自动清理，因此也不会删除被并发进程替换的
目录或文件。升级脚手架时，应生成到新的同级目录并审查 Diff。

生成操作本身不需要网络。Fabric 或 BepInEx 第一次构建可能访问固定的 Gradle、
Maven 或 NuGet Source 以取得依赖。Wrapper Distribution、依赖版本和 Lock File
都由模板固定；不要仅为让 Restore 成功就将其改成浮动版本。

## 快速开始

以下示例使用通用公开标识符。请在允许容纳新项目的目录中运行。

### Fabric

```bash
rin init mod \
  --host fabric \
  --id guide_npc \
  --name "Guide NPC" \
  --namespace io.github.example \
  --author example \
  --output guide_npc

cd guide_npc
./gradlew clean build --no-daemon
```

Windows PowerShell：

```powershell
rin.exe init mod --host fabric --id guide_npc --name "Guide NPC" `
  --namespace io.github.example --author example --output guide_npc
Set-Location guide_npc
.\gradlew.bat clean build --no-daemon
```

Fabric 模板固定 Minecraft `1.21.1`、Fabric Loader `0.16.14`、Fabric API
`0.116.14+1.21.1`、Loom `1.11.8`、Gradle `8.14.3` 与 Java 21。它会
Vendor Rin Java SDK 并构建 Server-side Mod。不要在没有重新执行构建与真实
Server Gate 的情况下，静默替换这一已测试组合中的某个成员。
Gradle 许可与 Notice 只适用于脚手架分发的 Wrapper，不替生成的 Mod 授权。

### BepInEx Mono

```bash
rin init mod \
  --host bepinex-mono \
  --id guide_npc \
  --name "Guide NPC" \
  --namespace io.github.example \
  --output guide_npc_mono

cd guide_npc_mono
dotnet restore GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests/bin/Release/net6.0/GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.Mono/GuideNpc.Mono.csproj --locked-mode
dotnet build GuideNpc.Mono/GuideNpc.Mono.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-mono-0.1.0.zip
```

Windows PowerShell 使用相同的 `dotnet` 命令：

```powershell
rin.exe init mod --host bepinex-mono --id guide_npc --name "Guide NPC" `
  --namespace io.github.example --output guide_npc_mono
Set-Location guide_npc_mono
dotnet restore GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests\bin\Release\net6.0\GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.Mono\GuideNpc.Mono.csproj --locked-mode
dotnet build GuideNpc.Mono\GuideNpc.Mono.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-mono-0.1.0.zip
```

模板以 `netstandard2.0` 为 Target，并固定上游 BepInEx 6 Mono Package
`6.0.0-be.785`。BepInEx 6 仍属于尚未正式发布的 Bleeding-edge 系列；
编译成功不能证明 Plugin 会在某一具体 Unity 游戏中加载。打包 Helper 会执行
Locked Publish，强制包含 `System.Text.Json.dll` 及其完整的固定 Managed
Dependency Set 和 `LICENSE-RIN.txt`，拒绝 Mono 受审查 Allowlist 之外的全部
DLL，并在 ZIP
Manifest 记录每个安装文件的 Checksum；同时携带经过审查的 .NET 许可/Notice
Set，并在复验时强制检查每份 Notice 的受控 SHA-256。该 Helper 支持 Python
3.9 及更高版本。新增 Managed Dependency 时，必须先审查 Runtime File 与再分发
许可，才能修改 Allowlist。

### BepInEx IL2CPP

```bash
rin init mod \
  --host bepinex-il2cpp \
  --id guide_npc \
  --name "Guide NPC" \
  --namespace io.github.example \
  --output guide_npc_il2cpp

cd guide_npc_il2cpp
dotnet restore GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests/GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests/bin/Release/net6.0/GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.IL2CPP/GuideNpc.IL2CPP.csproj --locked-mode
dotnet build GuideNpc.IL2CPP/GuideNpc.IL2CPP.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-il2cpp-0.1.0.zip
```

Windows PowerShell：

```powershell
rin.exe init mod --host bepinex-il2cpp --id guide_npc --name "Guide NPC" `
  --namespace io.github.example --output guide_npc_il2cpp
Set-Location guide_npc_il2cpp
dotnet restore GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj --locked-mode
dotnet build GuideNpc.Core.Tests\GuideNpc.Core.Tests.csproj -c Release --no-restore --nologo
dotnet exec GuideNpc.Core.Tests\bin\Release\net6.0\GuideNpc.Core.Tests.dll
dotnet restore GuideNpc.IL2CPP\GuideNpc.IL2CPP.csproj --locked-mode
dotnet build GuideNpc.IL2CPP\GuideNpc.IL2CPP.csproj --configuration Release --no-restore --nologo
python package_bepinex.py
python package_bepinex.py --verify-archive dist/guide-npc-bepinex-il2cpp-0.1.0.zip
```

模板以 .NET 6 为 Target，并固定 BepInEx `6.0.0-be.785`。它刻意不 Vendor
游戏专属的 Generated Interop Assembly。尝试应用效果前，必须向一款具体游戏安装
正确的 BepInEx Build，让该游戏生成 Interop 文件，并实现 Owning-thread Hook。
Helper 只打包三个受审查的项目 DLL 与 `LICENSE-RIN.txt`；Archive
复验会拒绝所有额外 DLL，包括 BepInEx、Unity 或游戏专属 Interop Runtime。
扩展 Allowlist 前必须审查 Runtime File 与再分发许可。
由于不再分发那 8 个 DLL，它会有意省略 Mono 专用的 .NET 许可/Notice Set。

两个 BepInEx 安装包都会固定 ZIP Timestamp、排序、Unix Regular-file Mode 与
Creator Metadata。复验会在交付解压前拒绝目录、符号链接、加密、超长、
Path Traversal、设备名、大小写冲突及未写入 Manifest 的条目。

### Luanti

```bash
rin init mod \
  --host luanti \
  --id guide_npc \
  --name "Guide NPC" \
  --author example \
  --output guide_npc

cd guide_npc
luac5.1 -p init.lua
luac5.1 -p state.lua
luac5.1 -p rin.lua
lua5.1 test_state.lua
luac5.4 -p init.lua
luac5.4 -p state.lua
luac5.4 -p rin.lua
lua5.4 test_state.lua
```

Windows 使用对应的已安装 `luac.exe` 与 `lua.exe` 命令。生成 Mod 会 Vendor Rin
Lua SDK，并保持语法与状态测试同时兼容 Lua 5.1 和 5.4。把生成的 Mod ID 加入
`secure.http_mods`，再在真实 Luanti Headless Server 中重复生命周期测试。
生成器不会写入 `mod.conf.release`；该字段由 ContentDB 所有。

## 必须完成的游戏专属工作

生成的 README 会标出以下权威边界。每一项都被替换并审查之前，Mod 不应分发：

1. **稳定存档身份。** 从真实 World、Profile 或 Save Slot 派生 Session 身份。
   不要使用可执行文件路径、Process ID、时钟或每次启动都变化的新随机值。
2. **动作白名单。** 只发送游戏创作的 Action ID，并在 Apply 前校验每个返回 ID
   与参数。生成文本只是显示数据，不是 Command、Item ID、Reflection Target 或
   Filesystem Path。
3. **Owning-thread Apply。** Fabric 工作编组到 Server Thread，BepInEx 工作
   编组到游戏拥有的 Unity Thread，Luanti 工作通过其调度的 Server Callback
   执行。不得在 Render 或 Server-tick Loop 中阻塞等待网络。
4. **可信 Content Binding。** 从正在运行且由游戏所有的 Content Manifest
   计算 `content_hash`。不得从导入的存档、Snapshot 或模型响应复制 Expected
   Hash 或 Binding。
5. **持久 Workflow 恢复。** 持久化完整 Pending Turn、已接受的 Job ID、
   Operation Marker 与 Outcome Outbox；接受新 Turn 前先 Resume 或 Drain。
   含糊的 Timeout 或 Cancellation 必须 Fail Closed。
6. **Sidecar 生命周期。** 明确由 Launcher、Dedicated Server 还是游戏安装启动
   Rin；使用每用户可写数据目录、等待 Health、强制每个数据目录只有一个 Writer，
   并执行有界 Shutdown。模型 Provider 凭据只保存在 Sidecar；通过进程环境传递
   `RIN_TOKEN`，不要写进存档或提交到仓库的 Mod Config。

模板刻意只使用可逆的 Dialogue、Wait 或 Refusal 效果，并保持 `advisory`。
发物品、货币、任务推进、背包变化与世界编辑需要按 Operation ID 幂等的游戏 API，
或一个把游戏效果、Applied Marker 与持久 Outcome 一起 Commit 的真实事务。参见
[宿主持久保证分级](host-durability.zh-CN.md)。

## 实施与验收清单

### 本次脚手架交付

- [x] 注册 `init mod`、`--list-hosts`、四个精确 Host 名，以及可操作的 Help
  与错误输出。
- [x] 写入前执行 Host-aware ID、显示名、Namespace、语义版本、输出路径、
  Windows 名称、大小写冲突和符号链接校验。
- [x] 在 Rin Binary 内嵌模板与完整 Java、C# 或 Lua SDK 源码；生成排序后的
  SHA-256 Manifest，并保留必要的脚本 Mode。
- [x] 保证不覆盖、不穿越、Dry-run 与真实生成确定性一致；失败后不自动清理，
  保留 Incomplete Marker 供人工审查，并且不删除并发替换的目录或文件。
- [x] 在 Linux 与 Windows 构建生成的 Fabric 和两个 BepInEx 项目；构建并打包
  每个 BepInEx Backend，独立复验两个安装 ZIP；使用 Lua 5.1 与 5.4
  解析并运行 Luanti 生成结果。
- [x] 生成 README 必须明确固定依赖、未完成的游戏所有 TODO、Preview 状态与
  `advisory` 能力边界。

这份清单是验收契约。只有对应代码与自动测试已经落地时才能勾选；文档本身不是证据。

### 后续真实游戏验收

- [ ] 在 Linux 与 Windows 的真实 Minecraft `1.21.1` Dedicated Server 中
  加载生成 JAR；测试两个 World、保存/停止、强杀、并发玩家与 Sidecar 重启。
- [ ] 在一款具名代表性游戏中加载生成的 Mono Plugin，并替换演示 Save Identity
  与 Main-thread Effect Hook。
- [ ] 在一款具体游戏完成 Interop 生成后加载 IL2CPP Plugin；测试 AOT 行为、
  Unload、Restart 与实际 Game Hook。
- [ ] 在真实 Luanti Headless Server 加载生成 Mod；测试 `secure.http_mods`、
  真实 ModStorage 保存周期、并发玩家、`/shutdown` 与强杀。
- [ ] 对每个 Release 声明的 Host/Backend 执行共享崩溃矩阵，以及文档要求的
  至少两小时或 1,000 Turn Preview Soak Gate。

## 官方宿主参考

- [Fabric `fabric.mod.json` 规范](https://docs.fabricmc.net/develop/loader/fabric-mod-json)
- [Fabric Example Mod](https://github.com/FabricMC/fabric-example-mod)
- [BepInEx 6 Plugin Setup](https://docs.bepinex.dev/master/articles/dev_guide/plugin_tutorial/1_setup.html)
- [BepInEx IL2CPP 安装](https://docs.bepinex.dev/master/articles/user_guide/installation/unity_il2cpp.html)
- [Luanti Mod 布局与 `mod.conf`](https://api.luanti.org/mods/)
- [Luanti HTTP API](https://docs.luanti.org/for-creators/api/http-api/)
- [Windows 文件与路径命名规则](https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file)
- [Semantic Versioning 2.0.0](https://semver.org/)

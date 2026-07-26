package modscaffold

import (
	"fmt"
	"strings"
)

func readmeEnglish(options normalizedOptions) string {
	host := resolvedHost(options)
	var builder strings.Builder
	fmt.Fprintf(&builder, `# %s

[English](README.md) | [简体中文](README.zh-CN.md)

This is an offline, deterministic Rin Mod scaffold for **%s**. It vendors the
complete %s Rin SDK source used by Rin %s and pins the host dependencies listed
in [rin-scaffold.json](rin-scaffold.json).

> Build or harness success is not proof of stability in a real game. This
> scaffold declares only the Rin "advisory" durability profile. Complete
> the game-specific integration and real-host validation before release.

## Project

- Mod ID: %s
- Mod version: %s
- Host: %s
- Template status: %s
`,
		escapeMarkdown(options.Name),
		escapeMarkdown(host.Name),
		host.Language,
		escapeMarkdown(options.RinVersion),
		markdownCode(options.ID),
		markdownCode(options.Version),
		markdownCode(host.ID),
		markdownCode(host.TemplateStatus),
	)
	if options.Namespace != "" {
		fmt.Fprintf(&builder, "- Owner namespace: %s\n", markdownCode(options.Namespace))
	}
	if options.Author != "" {
		fmt.Fprintf(&builder, "- Author: %s\n", markdownCode(options.Author))
	}
	if options.JavaPackage != "" && options.Host == HostFabric {
		fmt.Fprintf(&builder, "- Java package: %s\n", markdownCode(options.JavaPackage))
	}
	if options.PluginGUID != "" && strings.HasPrefix(options.Host, "bepinex-") {
		suffix := ".mono"
		if options.Host == HostBepInExIL2CPP {
			suffix = ".il2cpp"
		}
		fmt.Fprintf(&builder, "- BepInEx plugin GUID: %s\n",
			markdownCode(options.PluginGUID+suffix))
	}
	builder.WriteString(`
## Verify the generated project

The generator itself is offline. A first Gradle or NuGet build may download the
exact pinned dependencies.

macOS/Linux:

~~~bash
`)
	for _, command := range host.UnixVerifyCommands {
		builder.WriteString(command)
		builder.WriteByte('\n')
	}
	builder.WriteString("~~~\n\nWindows PowerShell:\n\n~~~powershell\n")
	for _, command := range host.WindowsVerifyCommands {
		builder.WriteString(command)
		builder.WriteByte('\n')
	}
	builder.WriteString("~~~\n")
	builder.WriteString(hostNotesEnglish(options))
	builder.WriteString(`
## Required game-specific work

1. Replace every demo observation, actor, and action with bounded game data.
2. Derive a stable save/profile identity from the game, never from an install
   path, player display name, or a per-process random value.
3. Replace the all-zero Binding content hash with a trusted content-manifest
   hash and update the content version deliberately.
4. Keep an explicit action allowlist. Validate authority and freshness, then
   apply accepted actions on the game's owning thread.
5. Preserve Pending Turn, applied-operation markers, and the Outcome Outbox in
   the authoritative save boundary. Do not claim a stronger durability profile
   without crash evidence.
6. Start and stop the Rin Sidecar with the game or document an external service
   lifecycle. Keep "RIN_TOKEN" out of saves, logs, and generated files.

Run the applicable checklist in the
[real-host validation guide](https://github.com/sunrioa/rin/blob/main/docs/mod-integration-validation.md).
The long-run gate is at least two hours or 1,000 turns, including restart and
fault injection.

## Updating Rin

The vendored SDK is intentionally a complete, immutable copy. Do not edit it
through broad search-and-replace. Generate a fresh scaffold with the new Rin
binary, inspect "rin-scaffold.json" and the migration guide, then merge the
host changes. The generator never overwrites an existing directory.

## License

"LICENSE-RIN.txt" covers the vendored Rin SDK and Rin-derived template
code. It does not choose a license for your original game or Mod content.
`)
	builder.WriteString(thirdPartyLicenseNotesEnglish(options))
	return builder.String()
}

func readmeChinese(options normalizedOptions) string {
	host := resolvedHost(options)
	var builder strings.Builder
	fmt.Fprintf(&builder, `# %s

[English](README.md) | [简体中文](README.zh-CN.md)

这是面向 **%s** 的离线、确定性 Rin Mod 脚手架。它内置 Rin %s 使用的完整
%s SDK 源码，并把宿主依赖固定在 [rin-scaffold.json](rin-scaffold.json) 中。

> 构建或 Harness 通过不等于已在真实游戏稳定运行。该脚手架只声明 Rin
> "advisory" 能力等级；发布前仍需完成游戏专属接入和真实宿主验收。

## 项目信息

- Mod ID：%s
- Mod 版本：%s
- 宿主：%s
- 模板状态：%s
`,
		escapeMarkdown(options.Name),
		escapeMarkdown(host.Name),
		escapeMarkdown(options.RinVersion),
		host.Language,
		markdownCode(options.ID),
		markdownCode(options.Version),
		markdownCode(host.ID),
		markdownCode(host.TemplateStatus),
	)
	if options.Namespace != "" {
		fmt.Fprintf(&builder, "- 所有者命名空间：%s\n", markdownCode(options.Namespace))
	}
	if options.Author != "" {
		fmt.Fprintf(&builder, "- 作者：%s\n", markdownCode(options.Author))
	}
	if options.JavaPackage != "" && options.Host == HostFabric {
		fmt.Fprintf(&builder, "- Java Package：%s\n", markdownCode(options.JavaPackage))
	}
	if options.PluginGUID != "" && strings.HasPrefix(options.Host, "bepinex-") {
		suffix := ".mono"
		if options.Host == HostBepInExIL2CPP {
			suffix = ".il2cpp"
		}
		fmt.Fprintf(&builder, "- BepInEx Plugin GUID：%s\n",
			markdownCode(options.PluginGUID+suffix))
	}
	builder.WriteString(`
## 校验生成工程

生成过程本身完全离线。第一次执行 Gradle 或 NuGet 构建时，可能需要下载已经固定
版本的依赖。

macOS/Linux：

~~~bash
`)
	for _, command := range host.UnixVerifyCommands {
		builder.WriteString(command)
		builder.WriteByte('\n')
	}
	builder.WriteString("~~~\n\nWindows PowerShell：\n\n~~~powershell\n")
	for _, command := range host.WindowsVerifyCommands {
		builder.WriteString(command)
		builder.WriteByte('\n')
	}
	builder.WriteString("~~~\n")
	builder.WriteString(hostNotesChinese(options))
	builder.WriteString(`
## 必须完成的游戏专属接入

1. 把演示 Observation、Actor 和 Action 替换为有界的真实游戏数据。
2. 从游戏存档或 Profile 获取稳定身份；不得使用安装路径、玩家显示名或进程级
   随机值。
3. 用可信内容清单哈希替换全零 Binding content hash，并有意识地更新内容版本。
4. 保持显式动作白名单。校验权威和 Freshness 后，在游戏所属线程应用动作。
5. 在权威存档边界保存 Pending Turn、已应用 Operation Marker 与 Outcome Outbox。
   未经崩溃证据不得声称更强的能力等级。
6. 让 Rin Sidecar 随游戏启停，或明确记录外部服务生命周期。不得把
   "RIN_TOKEN" 写入存档、日志或生成文件。

请执行[真实宿主验收指南](https://github.com/sunrioa/rin/blob/main/docs/mod-integration-validation.zh-CN.md)
中对应清单。长稳门禁至少为两小时或 1,000 Turn，并包含重启与故障注入。

## 升级 Rin

内置 SDK 是有意保留的完整、不可变副本。不要对它执行宽泛字符串替换。使用新版
Rin 二进制生成一个全新目录，检查 "rin-scaffold.json" 和迁移指南后，再
合并宿主改动。生成器永远不会覆盖已有目录。

## 许可证

"LICENSE-RIN.txt" 只覆盖内置 Rin SDK 与源自 Rin 的模板代码；它不会替
你的原创游戏或 Mod 内容选择许可证。
`)
	builder.WriteString(thirdPartyLicenseNotesChinese(options))
	return builder.String()
}

func thirdPartyLicenseNotesEnglish(options normalizedOptions) string {
	switch options.Host {
	case HostFabric:
		return `
"LICENSE-GRADLE.txt" and "NOTICE-GRADLE.txt" are the exact notices shipped
with the pinned Gradle 8.14.3 distribution and cover the redistributed Gradle
Wrapper only; they do not license your Mod.
`
	case HostBepInExMono:
		return `
The reviewed files under "third-party/" cover only the eight pinned .NET
runtime libraries redistributed by the Mono install ZIP. The packager verifies
their approved SHA-256 digests and includes them in the ZIP manifest. They do
not license your Mod or apply to an IL2CPP package.
`
	case HostBepInExIL2CPP:
		return `
This IL2CPP scaffold does not redistribute the Mono-only .NET dependency set,
so it deliberately contains and packages none of the Mono third-party notices.
`
	default:
		return ""
	}
}

func thirdPartyLicenseNotesChinese(options normalizedOptions) string {
	switch options.Host {
	case HostFabric:
		return `
"LICENSE-GRADLE.txt" 与 "NOTICE-GRADLE.txt" 是固定 Gradle 8.14.3
Distribution 随附的精确 Notice，只覆盖脚手架再分发的 Gradle Wrapper，不会
替你的 Mod 授权。
`
	case HostBepInExMono:
		return `
"third-party/" 下经过审查的文件只覆盖 Mono 安装 ZIP 再分发的 8 个固定 .NET
Runtime Library。打包器会复验这些文件的受控 SHA-256，并把它们写入 ZIP
Manifest；它们不会替你的 Mod 授权，也不适用于 IL2CPP 安装包。
`
	case HostBepInExIL2CPP:
		return `
该 IL2CPP 脚手架不再分发 Mono 专用 .NET Dependency Set，因此有意不生成、也
不打包 Mono 第三方 Notice。
`
	default:
		return ""
	}
}

func resolvedHost(options normalizedOptions) HostDescriptor {
	host := cloneHost(options.HostDescriptor)
	host.UnixVerifyCommands = replaceCommands(host.UnixVerifyCommands, options.CodeName)
	host.WindowsVerifyCommands = replaceCommands(host.WindowsVerifyCommands, options.CodeName)
	return host
}

func hostNotesEnglish(options normalizedOptions) string {
	switch options.Host {
	case HostFabric:
		return fmt.Sprintf(`
The output is specifically pinned to Minecraft 1.21.1, Java 21, and the listed
Fabric toolchain; it does not claim compatibility with other Minecraft
versions. The demonstration command is "/%s". Install the release JAR
from "build/libs/" plus the matching Fabric API in a dedicated-server
test instance. Customize Binding/Actor/Action construction in
"RinNpcRequests.java" and the server observation, policy, and apply hook in
"RinNpcMod.java"; keep "FabricWorkflowStore.java" as the recovery boundary.
`, options.CommandName)
	case HostBepInExMono:
		return fmt.Sprintf(`
This is a single-backend BepInEx 6 Mono project. BepInEx 6 is still treated as
bleeding-edge by this Rin release. The F8 demo defaults to disabled. Connect a
real save identity and game-owned Apply hook before enabling it. Customize
identity and owning-thread dispatch in "%s.Mono/Plugin.cs", then edit
Binding/Actor/Action policy in "%s.Core/RinNpcRuntime.cs".

### Build an install package

~~~bash
python package_bepinex.py
python package_bepinex.py --verify-archive dist/%s-bepinex-mono-%s.zip
~~~

The local helper performs locked restore and publish, then creates a
deterministic Windows-safe ZIP using Python 3.9 or newer. It requires
"%s.Mono.dll", "%s.Core.dll",
"Rin.Client.dll", "System.Text.Json.dll", and every pinned managed dependency
needed by that JSON stack. It includes "LICENSE-RIN.txt", records each install
file's SHA-256 checksum in "manifest.json", includes and content-pins the
reviewed .NET license and third-party notices under "third-party/", and rejects
every DLL outside the reviewed allowlist, including BepInEx and Unity runtime
assemblies. Review runtime files and redistribution notices before extending
that allowlist. Extract the verified ZIP into the game root; it installs below
"BepInEx/plugins/%s".
`, options.CodeName, options.CodeName, options.CommandName, options.Version,
			options.CodeName, options.CodeName, options.CodeName)
	case HostBepInExIL2CPP:
		return fmt.Sprintf(`
This is a single-backend BepInEx 6 IL2CPP transport project. It intentionally
contains no guessed generated Interop assemblies. Register a game-specific
owning-thread "ApplyDialogue" delegate before requesting any turn, and
never bundle BepInEx, Unity, or game Interop runtime assemblies as Mod payload.
The project is Preview until tested in the target Player. Register the hook in
"%s.IL2CPP/Plugin.cs" and edit Binding/Actor/Action policy in
"%s.Core/RinNpcRuntime.cs".

### Build an install package

~~~bash
python package_bepinex.py
python package_bepinex.py --verify-archive dist/%s-bepinex-il2cpp-%s.zip
~~~

The local helper performs locked restore and publish, packages only the managed
DLLs emitted for this .NET 6 plugin, and creates a deterministic Windows-safe
ZIP using Python 3.9 or newer. It requires "%s.IL2CPP.dll", "%s.Core.dll", and
"Rin.Client.dll", while
including "LICENSE-RIN.txt", recording each install file's SHA-256 checksum in
"manifest.json", and rejecting every DLL outside the three reviewed project
assemblies, including BepInEx, Unity, and game-specific Interop runtimes.
Review runtime files and redistribution notices before extending the allowlist.
Extract the verified ZIP into the game root; it installs below
"BepInEx/plugins/%s".
`, options.CodeName, options.CodeName, options.CommandName, options.Version,
			options.CodeName, options.CodeName, options.CodeName)
	case HostLuanti:
		return fmt.Sprintf(`
Install the directory under the exact Mod name "%s", then add
"secure.http_mods = %s" to "minetest.conf". The transport remains
loopback-only because the Luanti Mod HTTP API cannot enforce Rin's
no-redirect authenticated remote-origin boundary. Test with a currently
security-supported Luanti release. Customize the Binding, actors, action
allowlist, observations, and apply callback in "init.lua"; keep "state.lua" as
the ModStorage recovery boundary.
`, options.ID, options.ID)
	default:
		return ""
	}
}

func hostNotesChinese(options normalizedOptions) string {
	switch options.Host {
	case HostFabric:
		return fmt.Sprintf(`
该输出只固定到 Minecraft 1.21.1、Java 21 与清单中的 Fabric 工具链；不声称兼容
其他 Minecraft 版本。演示命令为 "/%s"。请把 "build/libs/" 下的
Release JAR 和匹配的 Fabric API 安装到专用 Dedicated Server 测试实例。
在 "RinNpcRequests.java" 定制 Binding/Actor/Action 构造，在 "RinNpcMod.java"
定制 Server Observation、Policy 与 Apply Hook；保留 "FabricWorkflowStore.java"
作为恢复边界。
`, options.CommandName)
	case HostBepInExMono:
		return fmt.Sprintf(`
这是单一 Backend 的 BepInEx 6 Mono 工程。本 Rin 版本仍把 BepInEx 6 视为
Bleeding-edge。F8 演示默认关闭；接入真实 Save Identity 和游戏所属线程 Apply
Hook 后才能启用。在 "%s.Mono/Plugin.cs" 定制身份和 Owning-thread Dispatch，
在 "%s.Core/RinNpcRuntime.cs" 定制 Binding/Actor/Action Policy。

### 构建安装包

~~~bash
python package_bepinex.py
python package_bepinex.py --verify-archive dist/%s-bepinex-mono-%s.zip
~~~

本地 Helper 会执行 Locked Restore 与 Publish，然后生成确定性、Windows-safe 的
ZIP；需要 Python 3.9 或更高版本。它强制要求 "%s.Mono.dll"、"%s.Core.dll"、
"Rin.Client.dll"、
"System.Text.Json.dll" 以及该 JSON Stack 固定的全部必要 Managed Dependency，
同时包含 "LICENSE-RIN.txt"、在 "manifest.json" 记录每个安装文件的 SHA-256
Checksum，也包含并复验 "third-party/" 下受审查的 .NET License 与第三方
Notice，并拒绝受审查 Allowlist 之外的全部 DLL（包括 BepInEx 与 Unity Runtime
Assembly）。新增 Managed Dependency 时，必须先审查其 Runtime File 与再分发
Notice，再扩展 Allowlist。把通过复验的 ZIP 解压到游戏根目录；内容会安装到
"BepInEx/plugins/%s"。
`, options.CodeName, options.CodeName, options.CommandName, options.Version,
			options.CodeName, options.CodeName, options.CodeName)
	case HostBepInExIL2CPP:
		return fmt.Sprintf(`
这是单一 Backend 的 BepInEx 6 IL2CPP Transport 工程。它不会猜测或捆绑生成的
Interop Assembly。请求 Turn 前必须注册游戏专属、回到所属线程的
`+"`ApplyDialogue`"+` Delegate；不得把 BepInEx、Unity 或游戏 Interop Runtime
Assembly 打进 Mod。进入目标 Player 实测前该工程仍为 Preview。在
"%s.IL2CPP/Plugin.cs" 注册 Hook，在 "%s.Core/RinNpcRuntime.cs"
定制 Binding/Actor/Action Policy。

### 构建安装包

~~~bash
python package_bepinex.py
python package_bepinex.py --verify-archive dist/%s-bepinex-il2cpp-%s.zip
~~~

本地 Helper 会执行 Locked Restore 与 Publish，只打包该 .NET 6 Plugin 实际输出的
Managed DLL，并使用 Python 3.9 或更高版本生成确定性、Windows-safe 的 ZIP。它强制要求
"%s.IL2CPP.dll"、"%s.Core.dll" 与 "Rin.Client.dll"，包含
"LICENSE-RIN.txt"、在 "manifest.json" 记录每个安装文件的 SHA-256 Checksum，
并拒绝三个受审查项目 Assembly 之外的全部 DLL（包括 BepInEx、Unity 和游戏专属
Interop Runtime）。扩展 Allowlist 前必须审查 Runtime File 与再分发 Notice。
把通过复验的 ZIP 解压到游戏根目录；内容会安装到 "BepInEx/plugins/%s"。
`, options.CodeName, options.CodeName, options.CommandName, options.Version,
			options.CodeName, options.CodeName, options.CodeName)
	case HostLuanti:
		return fmt.Sprintf(`
请把目录安装为精确 Mod 名 "%s"，再向 "minetest.conf" 添加
"secure.http_mods = %s"。Luanti Mod HTTP API 无法落实 Rin 对远程鉴权
Origin 的禁止重定向边界，因此该 Transport 保持仅 Loopback。请使用仍接受安全
支持的 Luanti 版本验证。在 "init.lua" 定制 Binding、Actor、Action 白名单、
Observation 与 Apply Callback；保留 "state.lua" 作为 ModStorage 恢复边界。
`, options.ID, options.ID)
	default:
		return ""
	}
}

func escapeMarkdown(value string) string {
	return strings.NewReplacer(`\`, `\\`, "`", "\\`").Replace(value)
}

func markdownCode(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "\\`") + "`"
}

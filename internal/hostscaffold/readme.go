package hostscaffold

import (
	"fmt"
	"strings"
)

func readmeEnglish(options normalizedOptions) string {
	return fmt.Sprintf(`# %s

[简体中文](README.zh-CN.md)

This is an offline, deterministic Rin Host contract skeleton for the %s
runtime. It is engine-neutral and intentionally contains no game-specific
executor, model provider, API key, or background service.

## Project

- ID: %s
- Version: %s
- Rin: %s
- Host contract: %s
- Runtime: %s
- Validation: real-game validation is required
%s
## Generated files

- "rin-host.json" identifies the Host contract and capability directory.
- "capabilities/dialogue.say.json" is a sealed example capability.
- "src/README.md" defines the engine-owned integration boundaries.
- "rin-scaffold.json" records deterministic file digests.

## Integration order

1. Map stable Host, world, actor, epoch, and observation identities from the
   game save.
2. Publish only bounded observations and registered capability specs.
3. Bind each Action Request to its capability digest, arguments, target refs,
   observation sequence, and current epoch.
4. Apply policy and controller-authority checks immediately before executing
   on the game authority thread.
5. Report progress and one authoritative terminal Action Outcome with the
   supplied operation identity.

Model text is untrusted input. Never expose an eval surface, process launcher,
console command, engine object, secret, or unrestricted file/network access as
an ordinary capability. Elevated capabilities require explicit game-owned
policy and confirmation.

## Verify

~~~bash
rin conformance host
rin doctor host
~~~

These checks validate the portable contract skeleton. They do not replace
real-game tests for authority-thread execution, cancellation, restart,
idempotency, multiplayer policy, or emergency stop.

## License

"LICENSE-RIN.txt" covers Rin-derived scaffold files. It does not choose a
license for the game or Mod that consumes them.
`, escapeMarkdown(options.Name), markdownCode(options.HostDescriptor.Language),
		markdownCode(options.ID), markdownCode(options.Version),
		markdownCode(options.RinVersion), markdownCode("rin.host/v2"),
		markdownCode(options.Runtime), authorLineEnglish(options.Author))
}

func readmeChinese(options normalizedOptions) string {
	return fmt.Sprintf(`# %s

[English](README.md)

这是面向 %s Runtime 的离线、确定性 Rin Host 契约骨架。它不绑定具体游戏引擎，
也不会生成游戏专属执行器、模型 Provider、API Key 或后台服务。

## 项目信息

- ID：%s
- 版本：%s
- Rin：%s
- Host 契约：%s
- Runtime：%s
- 验收状态：必须在真实游戏中验证
%s
## 生成文件

- "rin-host.json"：声明 Host 契约与能力目录。
- "capabilities/dialogue.say.json"：一个经过密封的示例能力。
- "src/README.md"：游戏引擎必须实现的边界。
- "rin-scaffold.json"：记录确定性文件摘要。

## 接入顺序

1. 从同一存档映射稳定的 Host、世界、角色、Epoch 和 Observation 身份。
2. 只发布有界 Observation 与已注册的 Capability Spec。
3. 把每个 Action Request 绑定到能力摘要、参数、目标引用、Observation 序号和当前
   Epoch。
4. 在游戏权威线程执行前，再次检查策略与控制权。
5. 使用原 Operation 身份上报进度和唯一、权威的终态 Action Outcome。

模型文本是不可信输入。普通能力不得暴露 eval、进程启动、控制台命令、引擎对象、
密钥或无限制文件/网络访问；高权限能力必须由游戏自己的策略和确认机制显式开放。

## 校验

~~~bash
rin conformance host
rin doctor host
~~~

这些命令验证可移植的契约骨架，不能代替真实游戏中的权威线程、取消、重启、幂等、
多人策略和急停测试。

## 许可证

"LICENSE-RIN.txt" 只覆盖源自 Rin 的脚手架文件，不会替使用它的游戏或 Mod
选择许可证。
`, escapeMarkdown(options.Name), markdownCode(options.HostDescriptor.Language),
		markdownCode(options.ID), markdownCode(options.Version),
		markdownCode(options.RinVersion), markdownCode("rin.host/v2"),
		markdownCode(options.Runtime), authorLineChinese(options.Author))
}

func authorLineEnglish(author string) string {
	if author == "" {
		return ""
	}
	return "- Author: " + markdownCode(author) + "\n"
}

func authorLineChinese(author string) string {
	if author == "" {
		return ""
	}
	return "- 作者：" + markdownCode(author) + "\n"
}

func escapeMarkdown(value string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(value)
}

func markdownCode(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "\\`") + "`"
}

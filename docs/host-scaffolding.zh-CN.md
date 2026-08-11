# Host 脚手架

[English](host-scaffolding.md) | [简体中文](host-scaffolding.zh-CN.md)

`rin init host` 离线生成一个确定性的 `rin.host/v2` 契约骨架。它不会下载模板、
安装依赖、生成模型代码或假装已经接入某个游戏引擎。

## 创建

```bash
rin init host \
  -engine custom \
  -runtime java \
  -id my-game-host \
  -name "My Game Host" \
  -version 0.1.0 \
  -output ./my-game-host
```

支持的 Runtime：`go`、`javascript`、`python`、`csharp`、`java`、`lua`。
当前唯一 Engine 模板是 `custom`；它代表通用契约，不代表已经实现任何引擎。

先检查而不写文件：

```bash
rin init host -engine custom -runtime java -id my-game-host -dry-run
rin init host -list-hosts
```

目标目录必须不存在。生成器不会覆盖已有目录或文件，也不会自动选择另一个名称。

## 文件

```text
my-game-host/
  README.md
  README.zh-CN.md
  LICENSE-RIN.txt
  rin-host.json
  rin-scaffold.json
  capabilities/
    dialogue.say.json
  src/
    README.md
```

- `rin-host.json`：Schema 2、`rin.host/v2`、Runtime、Durability 与能力目录。
- `rin-scaffold.json`：生成器、项目和每个文件的 SHA-256；用于检测骨架漂移。
- `capabilities/dialogue.say.json`：经过密封的 `CapabilitySpec` 示例。
- `src/README.md`：必须由具体游戏实现的 Authority 边界。
- `LICENSE-RIN.txt`：只覆盖 Rin 生成的骨架，不替游戏或 Mod 选择许可证。

默认 Capability 只是示例，不能让游戏真正显示对白。你必须实现参数到游戏对象的
绑定、Effect Preview、权威线程执行和 Outcome。

## 验证命令

在生成目录或通过 `-project` 指定目录：

```bash
rin conformance host -project ./my-game-host
rin doctor host -project ./my-game-host
```

Conformance 检查：

- 目录为真实目录，不是符号链接；
- Manifest Schema、Host Contract 和项目身份；
- 生成文件 SHA-256、Windows 大小写冲突和可移植路径；
- `rin-host.json` 与 Manifest 一致；
- 每个 Capability Schema、Digest、Version、风险和执行限制；
- 同一 ID+Version 不重复。

Doctor 输出接入状态和后续工作。两条命令只证明契约骨架有效，不证明真实游戏行为。

## 接入顺序

1. 从游戏存档定义稳定 Host、World、Actor 和 Epoch。
2. 实现 Authority Dispatcher，所有世界读取和修改回到游戏线程。
3. 发布有界 Observation 与 Capability Catalog。
4. 实现 `Bind` 和 `Preview`，不在此阶段修改世界。
5. 为 Effect 配置 Known Kind/Scope、Rule、Budget 和确认。
6. 实现幂等 `Execute`、`Cancel`、`Verify` 与 Outcome Outbox。
7. 连接 `rin-control`，完成 Host Register、Publish、Poll、ACK、Run 和 Outcome。
8. 运行故障注入和真实游戏验收。

## 安全属性

生成器在写入前一次性渲染所有文件并验证总大小、相对路径、大小写碰撞、保留名称、
符号链接和目标存在性。文件写入临时同级目录后原子发布；并发创建同一目标时只允许
一个成功。

`rin-scaffold.json` 是完整性清单，不是签名。能修改文件的人也能重算 SHA-256；
发布产物仍需使用你自己的签名和供应链机制。

## 不会生成的内容

- 引擎 SDK、Loader、Gradle、Unity Package 或 Unreal Plugin；
- 后台 Daemon 或 MCP Server；
- API Key、Token 或模型 Provider 配置；
- 任意命令、Shell、动态代码或游戏私有执行器；
- 已通过真实引擎测试的声明。

这种限制是刻意的。通用工具可以生成契约，但只有 Adapter 作者知道如何安全地读取
和修改具体游戏。

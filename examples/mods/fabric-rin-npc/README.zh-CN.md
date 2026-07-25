# Fabric Rin NPC 示例

[English](README.md) | [简体中文](README.zh-CN.md)

这是一个可直接构建的服务端参考项目，固定并共同测试 Minecraft `1.21.1`、
Fabric Loader `0.16.14`、Fabric API `0.116.14+1.21.1`、Loom `1.11.8`、
Gradle `8.14.3` 与 Java 21。这不表示源码无需修改就能兼容未来所有 Minecraft
版本。

## 构建与安装

Linux/macOS：

```bash
./gradlew clean build
```

Windows PowerShell 或命令提示符：

```bat
gradlew.bat clean build
```

把 `build/libs/rin-fabric-npc-0.6.0.jar` 和匹配版本的 Fabric API JAR 放入
专用服务器的 `mods` 目录。启动 Rin，并按需在服务器进程环境中设置
`RIN_URL`、`RIN_TOKEN`，然后由玩家执行 `/rin-npc ask`。Mod JAR 已包含 Rin
Java Client 类，不要再安装第二份 SDK JAR。

## 安全与恢复模型

**Host capability profile / 宿主能力 Profile：具有稳定身份的 `advisory`。**
Mod 在主世界 Saved Data
中保存生成一次的 World UUID、稳定序列、完整且身份不变的
Create/Observe/Propose 请求、Pending Turn/Job 身份和有上限的 Outcome
Outbox。同一存档重启后会恢复保留工作，不会创建新 Session。每个 Outbox
Entry 同时保存精确 Commit 和预先记录、仅含绝对事实的安全 Observe；只有明确
终态 Commit 错误才允许持久转换。

`PersistentState.markDirty()` 只是安排稍后存盘，不是同步的网络前持久屏障，
也不能把游戏修改与 Outbox 原子提交。因此本参考只提供可逆的聊天、等待和拒绝
动作，并如实保持 `advisory`。发放物品、推进任务或修改世界的 Plugin 必须证明
[宿主能力分级](../../../docs/host-capability-profiles.zh-CN.md)所要求的幂等或
事务边界。

结算前 Mod 会重新读取 Rin Session State，Java SDK 会校验 Proposal 仍在预期
Revision 上处于 Pending。State 缺失、过期、畸形或不可用都会 Fail Closed。
游戏只使用本地白名单 Action ID；模型文本不会成为命令、Item ID、反射目标或
世界修改。Minecraft API 和 Saved Data 访问都通过 `MinecraftServer.execute`
切回服务器线程。

宿主编排入口现为 250 行（原为 1,046 行）。Authored Protocol Payload、
Saved Data、`WorkflowStore` 和服务器线程调度分别位于有上限的独立类中，
Mod 作者可以单独审查或替换每个边界，无需复制 SDK 状态机。

存档上限为 256 个 Session、每个 Session 32 条报告、总 JSON 2,000,000 字符。
达到上限时 Mod 会停止新工作，不会静默丢弃恢复数据。升级 Preview 示例前请
备份世界，并确保 Mod 与 Sidecar 来自同一个 Rin Revision。

参考：[Fabric 示例 Mod](https://github.com/FabricMC/fabric-example-mod)、
[项目结构](https://docs.fabricmc.net/develop/getting-started/project-structure)、
[Saved Data](https://docs.fabricmc.net/develop/serialization/saved-data)。

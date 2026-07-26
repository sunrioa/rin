# Rin Godot 参考

[English](README.md) | [简体中文](README.zh-CN.md)

这是可直接运行的 Godot 4.6.3 项目，也是源码优先的接入 Kit：

- `rin_client.gd` 负责有界异步 HTTP 与 Proposal Job Transport；
- `rin_host_contract.gd` 校验完整 Decision Window/Offer Binding 与
  JSON-safe Host 值；
- `rin_workflow.gd` 负责稳定 Save Slot Identity、Authority Epoch、Pending
  Turn/Active Run 恢复、每槽并发、退出取消与 Outcome Outbox；
- `example_npc.gd` 是少于 250 行、由游戏拥有的 Policy 与 UI 示例。

用 Godot 4.6.3 打开本目录，在 `http://127.0.0.1:7374` 启动 Rin，运行场景，
再从游戏 UI 或 Debugger 调用 `ask_npc_to_respond()`。接入其他项目时，复制三个
`rin_*.gd` 文件，添加 `RinClient` 子节点，并为每个 Save Slot 创建一个
`RinWorkflow`。

默认槽以有界 JSON 保存到 `user://rin/default.json`。生成的 128-bit Run
ID、稳定 Create Request、World Identity、Host/World/Timeline Generation、
Sequence、逻辑 Tick High-water、完整 Pending Turn/Observe、Job ID、Active
Run 和最多 64 条 Outcome 会跨场景及进程重启恢复。新的 Host 生命周期会提升
Host/Timeline；权威世界替换或回滚后调用 `advance_epoch()`。Coordinator 在首次
请求前保存 Turn、轮询前保存 Job ID、调用游戏代码前保存 Active Run；重启时
Active Run 只产生一条 `outcome-unknown`，不会盲目重复效果。返回 Proposal 必须
精确匹配持久 Actor、Tick、Decision Window 与完整宿主 Offer。Report 重试期间
保持原样，ACK 也必须先持久化才能 Evict。

**Host durability profile：`advisory`。** `FileAccess.flush()` 后使用同目录
Target/Backup 双重 Rename，使中断替换可恢复并兼容 Windows 路径；但两次
Rename 不是一个原子操作，Godot 也无法把任意游戏世界效果纳入该文件事务。
不确定的崩溃因此报告 `outcome-unknown`；只有使用真正幂等的游戏操作或事务
存档 Provider 后，才能声明更强 Profile。

Client 只允许精确 Loopback Host 上的明文 HTTP，关闭 Redirect，限制响应体，
也不执行阻塞等待。远程 HTTPS Token 在运行时从 `RIN_TOKEN` 读取，而不是导出
到 Scene Property；Workflow State 永不保存它。`shutdown()` 会请求取消正在
执行的 Proposal Job；取消无法确认时，保留状态仍可在下次启动恢复。

运行固定版本的 Headless 验证：

```bash
python3 tools/verify_godot.py --godot /path/to/Godot
```

CI 下载 Godot 4.6.3 官方二进制并校验 GitHub Release Metadata 公布的
SHA-256，然后在 Linux 与 Windows 解析所有脚本，执行 Authority Generation、
旧 Offer 拒绝、保留 Job、精确 Outbox、Active Run、ACK、畸形状态和写失败
测试。本地验证命令有 30 秒硬超时，损坏的 Coroutine 不会永久挂住 CI。

参考：

- [Godot 命令行与 Headless 模式](https://docs.godotengine.org/en/stable/tutorials/editor/command_line_tutorial.html)
- [Godot 项目数据路径](https://docs.godotengine.org/en/stable/tutorials/io/data_paths.html)
- [Godot `DirAccess`](https://docs.godotengine.org/en/stable/classes/class_diraccess.html)

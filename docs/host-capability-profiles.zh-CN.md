# 宿主能力分级

[English](host-capability-profiles.md) | [简体中文](host-capability-profiles.zh-CN.md)

Rin 协调游戏进程与 Sidecar 之间的分布式流程，但世界状态只归游戏所有。每个
接入必须按动作类别声明下列一种 Profile。Profile 描述宿主真实的持久化与应用
边界，不取决于它调用了多少个 Rin API。

本文描述 Rin `0.6.0` Preview，不修改 `rin.protocol/v1` Wire Schema。

## Profile

| Profile | 宿主必须提供的保证 | 允许的动作类型 |
| --- | --- | --- |
| `advisory` | 建议使用稳定身份，但不声称拥有崩溃原子的应用边界。Pending Work 可以只是最终持久化，甚至只在内存。 | 只显示的建议、对白、可逆效果，或游戏明确接受重复/丢失的效果。 |
| `idempotent-action` | 网络提交前，完整 Pending Turn 已持久化。每个效果接收稳定 Operation ID，游戏能证明重复该 ID 不会重复应用效果。Report Outbox 已持久化。 | 由真正幂等的游戏 API 实现的持久效果。恢复是 At-least-once，Operation ID 使重复无害。 |
| `transactional-action` | 网络提交前，完整 Pending Turn 已持久化。一个宿主事务原子完成效果应用、Applied Operation 记录、精确 Outcome 入队与 Pending Turn 删除。Outbox 已持久化。 | 能参与同一权威事务的持久游戏效果；这是 Rin 最强的流程 Profile。 |

同一接入可以对不同动作使用不同 Profile。例如，对白可以是 `advisory`，而接收
Operation Key 的任务系统可以是 `idempotent-action`。能力协商必须 Fail
Closed：宿主无法提供动作所需的 Profile 时，不得把该动作加入候选列表。

调用 `setDirty()`、Save Slot 写入 API 或键值 Setter，本身不能证明下一次 HTTP
请求前字节已经进入持久介质。修改世界后再写 Marker 也不是原子事务。除非宿主
提供有文档的同步持久边界或按 Operation ID 幂等的应用 API，否则只能声明
`advisory`。

## 带版本的能力记录

SDK 与 Adapter 使用以下逻辑记录：

```text
HostCapabilities {
  version: 1
  profile: advisory | idempotent-action | transactional-action
  stable_identity: boolean
  durable_before_network: boolean
  durable_outbox: boolean
  idempotent_apply: boolean
  atomic_apply_and_outbox: boolean
}
```

SDK 会校验字段组合，而不是相信 Profile 标签：

- `advisory` 不作持久性承诺；
- `idempotent-action` 要求稳定身份、网络前持久化、持久 Outbox 与幂等应用；
- `transactional-action` 要求稳定身份、网络前持久化、持久 Outbox，以及效果、
  Marker、入队和删除的原子事务；
- `atomic_apply_and_outbox` 与 `idempotent_apply` 相互独立，宿主可以同时诚实
  支持两者。

Capability 是宿主本地事实，不发送给模型，也不授予执行权。候选动作白名单仍由
游戏编写。

## 稳定身份

稳定 Session 身份应派生自世界 UUID、Save Slot 与 Actor Key 等持久游戏身份。
不得使用进程启动时间、每次启动新建的 UUID、Frame Count、机器路径或 Bearer
Token。宿主没有现成身份时，应把最终的协议安全身份保存到存档。

改变 Content Binding 或有意开始新周目时可以创建新身份；重启同一世界不能。

## 恢复义务

提交前必须保存完整 Typed Propose Request、Request ID、Operation ID 和
Sequence。成功收到 `202` 后立即保存 Job ID。启动时及每个新 Turn 之前：

1. 排空保留的 Outcome Report；
2. 使用相同身份恢复保留的 Pending Turn；
3. 提交或轮询结果未知时 Fail Closed；
4. 通过声明 Profile 允许的宿主边界完成结算；
5. 保留工作解决后才启动新工作。

Coordinator 负责通用协议状态机。宿主负责稳定存储、权威 Apply Callback、引擎
线程调度和动作校验。

## 当前参考状态

仓库中的 Fabric、BepInEx、Luanti、Godot 与 Unity 示例都声明为 `advisory`。
Fabric 与 BepInEx 现在已有稳定身份和可重启的有界流程状态；其余参考会在后续
阶段完成各自的持久化改造。仅能在重启后恢复，并不能证明网络前持久或原子
Apply 边界。

Fabric Saved Data 用于跨 Session 保存，但 Mark Dirty 只会安排后续保存，不能
单独充当网络前持久屏障。Luanti ModStorage 按 `map_save_interval` 持久化，并且
可能使用 JSON 或 SQLite，Setter 同样不是同步崩溃边界。BepInEx 覆盖约束差异
很大的 Mono、IL2CPP 与 Target Framework；能力声明必须来自具体游戏 Plugin，
不能由 BepInEx 整体代替。参考状态文件使用 Flush + Replace 顺序，但无法把
任意游戏效果纳入同一个原子事务，因此不能据此提升到 `advisory` 以上。

## 审查清单

- Session 身份是否跨重启稳定，并且不依赖 Windows 路径语法？
- 宿主能否证明 POST 前 Pending Turn 已持久化？
- Apply 中断后能否重试而不重复效果？
- 效果、Marker、Outbox 入队和 Pending Turn 删除是否真正原子？
- 删除 Entry 前，Outbox ACK 是否已持久化？
- 关闭与保存 Hook 是否只在有界 Deadline 内等待？
- Model 文本与私有审计字段是否被排除在 Action Dispatch 之外？
- 文档是否明确写出真实 Profile，而不是暗示“Exactly Once”？

宿主权威参考：

- [Fabric Saved Data](https://docs.fabricmc.net/develop/serialization/saved-data)
- [Luanti ModStorage](https://docs.luanti.org/for-creators/api/classes/modstorage/)
- [BepInEx 6 Plugin 项目指南](https://docs.bepinex.dev/v6.0.0-pre.1/articles/dev_guide/plugin_tutorial/2_plugin_start.html)

# Host Contract

[English](host-contract.md) | [简体中文](host-contract.zh-CN.md)

`host` Go 包是 Rin 用来描述游戏宿主及其可安全开放操作的引擎无关边界。
Protocol v2 在 HTTP 边界复用其精确 Epoch、Capability、Offer、Invocation、
Run 与 Outcome Shape；本地注册和执行权仍属于游戏。

## 为什么需要这条边界

通用游戏接入不能假装所有引擎共享同一种 World、Tick、对象模型或导航系统；
可以统一的是安全决策所需的事实：

- `HostManifest` 声明权威、部署、控制、时钟、决策方式、Actor 并发和持久保证。
- `CapabilityDescriptor` 为一个带命名空间的能力声明精确语义版本、有界输入/输出
  JSON Schema、效果、执行形态、风险、权限、超时、取消和持久要求。
- `ActionOffer` 是游戏编写的候选动作，参数和目标已经绑定到权威 `Epoch`。
- `ActionInvocation` 把已接受 Offer 交给本地执行器，并在派发到权威线程之前执行
  最终 TOCTOU 授权检查。
- `ActionRun` 与 `ActionOutcome` 区分排队、执行、未决恢复和带证据的终态。
  Cancel 不等于 Rollback；即使能力标记为可逆，也仍需单独的补偿操作。

这组公共部分参考了 Unity ML-Agents 的动作契约、Unreal Gameplay Ability 的激活
方式、ROS 2 Actions 的长任务生命周期，以及 OpenSpiel/PettingZoo 的不同决策
时间模型。Rin 不依赖这些框架，也不复制它们各自的游戏世界模型。
[OpenSpiel 验证](open-spiel-validation.zh-CN.md)已经针对真实游戏 State 执行
顺序、同时、Chance 与隐藏信息映射。

## Discovery 不等于 Authority

`Registry.Snapshot` 只回答“这个宿主实现具备什么能力”，不会回答“这个 Actor
当前可以做什么”。

每轮由游戏创建有界 `ActionOffer`。Policy 只能选择 `offer_id`，不能自行生成
方法名、任意 JSON 参数、对象指针、控制台命令、Shell 或动态代码。宿主随后检查：

1. 精确 Capability ID 和语义版本仍然存在；
2. Descriptor SHA-256 Digest 没有变化；
3. 参数符合根节点封闭的 JSON Schema；
4. Offer 未过期且 Epoch 仍然匹配；
5. Capability 尚未被动态撤销；
6. 即将进入权威线程执行前，上述条件仍然成立。

因此同一 Registry 可以服务 Ren'Py Label、Unity Component、Unreal Ability、
Godot Node、服务端 Mod、Web 游戏和自研引擎，而不偏向其中任意一种宿主。

## Epoch 与对象引用

`Epoch` 包含稳定 Session/World ID 和三个正整数 Generation：

- `host`：宿主进程或权威实例被替换时变化；
- `world`：Scene、Map、Level、Shard 或等价世界重载时变化；
- `timeline`：存档分叉、Rollback、Rewind 或权威分支时变化。

它们不是渲染帧、物理帧、模拟 Step 或墙钟时间。`HostRef` 在 Adapter 外部是不透明
引用，只能由所属 Adapter 在引擎权威线程解析；Ephemeral Ref 不得持久化。

## Schema 与 Descriptor 规则

本包使用自包含 JSON Schema 2020-12。Schema 必须：

- 不超过 64 KiB，且根节点为 Object；
- 声明精确的 2020-12 `$schema` URI 和 `type: "object"`；
- 显式设置 `additionalProperties: false`；
- 不含重复 JSON Property Name，也不能加载外部引用。

需要封闭的嵌套 Object Schema 必须自行声明对应规则；Contract 强制封闭根节点，
不会擅自改写创作者提供的 Subschema。

实现对紧凑规范 JSON 计算 SHA-256，并把 Schema Hash 与运行限制绑定进第二层
Descriptor Digest。Schema 校验复用维护成熟的
[`santhosh-tekuri/jsonschema`](https://github.com/santhosh-tekuri/jsonschema)，
不在项目内维护一套不完整的 Schema Engine。

## Durability 是独立维度

已有[宿主持久保证分级](host-durability.zh-CN.md)只描述崩溃/重试持久保证：

- `advisory`：不承诺世界修改恢复；
- `idempotent-action`：持久 Pending Work/Outbox，并且应用可幂等；
- `transactional-action`：效果和 Outcome Outbox 可以原子发布。

它们不枚举 Gameplay Capability。Descriptor 声明所需最低持久级别，宿主达不到时
Registry 会拒绝注册。风险、权限、执行方式、取消和可逆性继续保持相互独立。

## 已交付边界

当前 Go 包已提供验证、确定性 Seal、并发注册/发现、动态撤销、Offer/Invocation/
Output 检查、动作状态转换，以及 Race/Fuzz 覆盖。Protocol v2 与各语言 SDK
承载类型化生命周期；[通用 Host SDK](host-sdk.zh-CN.md)进一步提供八个引擎侧
端口、持久 Pending Decision/ActionRun/Outbox、Authority Dispatch、精确重试与
Epoch 对账。生成 Host 工程由[通用 Host 脚手架指南](host-scaffolding.zh-CN.md)
单独说明。

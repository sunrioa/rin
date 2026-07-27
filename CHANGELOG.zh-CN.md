# 变更日志

[简体中文](CHANGELOG.zh-CN.md) | [English](CHANGELOG.md)

本文记录仓库级变更。Rin `0.7.0` 是 Preview 版本：它仍处于 pre-1.0 阶段，
后续 minor 版本之间会记录兼容变化，但不承诺始终保持兼容。

## 未发布

### 新增

- 引擎无关 Go `host` Contract，包含经过验证的 Host Manifest、权威 Epoch、
  不透明对象引用、带版本 Capability、游戏绑定 ActionOffer、Invocation、
  ActionRun 状态和 Outcome。
- 并发安全 Capability Registry，支持根节点封闭的 JSON Schema 2020-12 输入/输出、
  确定性 Descriptor Digest、动态撤销和执行前最终 TOCTOU 授权检查。
- Contract 的 Fuzz、Race、过期 Epoch/Offer、Digest 漂移、撤销、持久级别和
  动作状态转换测试。
- 使用官方 Luanti 5.16.1 Dedicated Server 验证源码 Mod 与生成的独立脚手架，
  覆盖真实 ModStorage userdata、持久 Authority Generation、完整 Offer Binding
  与中断 Active Run 恢复。
- 在 macOS/Linux/Windows 使用 SHA-256 固定的 OpenSpiel 2.0.1 验证顺序回合、
  原子同时 Window、宿主拥有的 Chance Transition 与隐藏信息 Noninterference。
- 新增供应商无关的派生 Memory Search、已批准文字 Speech、不可变 Audio Ref 与
  无内容 Telemetry 可选端口，并覆盖有界校验、取消、隐私与降级测试。
- 新增加速一年 File Store 回归：1,460 次 Observation、365 次
  Proposal/Outcome、每月 Snapshot、重启、历史检索、存储统计与最终 Session
  Archive。
- 新增 Authority Thread 非阻塞与 Recovery State Cleanup Host Scenario。
- Python、JavaScript、C#、Java 与 Lua Client 共用真实 Sidecar Conformance
  Corpus，覆盖严格 Wire 错误、Mutation Exact Retry、Timeout、Health 与成功
  Session Create。
- 新增 `store.OpenFileReadOnly` 与真正只读的 `rin inspect`；缺失或无效的 Revision
  Index 只在内存重建，且不暴露可选 Checkpoint、Lifecycle 与 Transfer 写能力。
- 新增 `protocol.NewHostValidatedPayload`，在 Observation 前校验 Host 自有 JSON
  Schema、精确 Digest、严格 JSON，并防御性复制 Data。

### 变化

- Wire Contract 升级为 `rin.protocol/v2`。Decision Window、完整绑定的 Action
  Offer、Epoch、类型化 Invocation/Run/Outcome Report 与
  `/v2/action/report[-batch]` 取代 v1 ActionSpec/Commit 语义。
- 删除 v1 Wire、Reducer 兼容分支、旧 Recovery Example、过时的 Semantic
  Baseline/Migration 文档与兼容 Alias。开发阶段用户应创建新 Lineage，或显式
  Export/Import。
- Unity 改为紧凑 `IRinUnityHost` 边界，保留任意 JSON Argument，并使用持久
  Pending Turn 与确切 Report Outbox。
- Runtime/Server 代码仍只依赖标准库；独立 Host Contract 使用经过维护的
  `santhosh-tekuri/jsonschema` 校验器。依赖许可记录在
  `THIRD-PARTY-NOTICES.md`。
- Capability Discovery 明确不等于动作授权：模型只能选择由游戏
  `ActionOffer` 预先绑定参数和目标的候选。
- JavaScript、C#、Java、内嵌脚手架资产与参考 Mod 中容易误导的
  `HostCapabilities`/`HostProfile` 已替换为
  `HostDurability`/`HostDurabilityProfile`。旧名称、错误码和文档路径被直接
  删除，不保留兼容 Alias。
- 删除 Luanti State Schema v1。Schema v2 绑定宿主提供的内容、显式保留 JSON 空集合、
  在 Server 重启时提升 Host/Timeline，并把中断效果对账为
  `outcome-unknown`，而不是重复执行。
- 公共决策与生成边界统一为 `DecisionProvider`、`DecisionContext`、
  `DecisionDraft` 与 `StructuredGenerationProvider`。旧 Go 类型名和无实际用途的
  Draft 自由文本字段被直接删除，不保留兼容 Alias。
- `Engine.Close(ctx)` 现在会拒绝新操作，并在调用方关闭 Store 前排空在途调用、
  Transfer Import 与异步 Checkpoint Worker；CLI 的全部退出路径均使用该顺序。
- OpenAPI 现在生成每条 HTTP Route 的 Request Schema Binding；HTTP Decoder
  直接消费该元数据，不再维护手写 Go Type Switch。
- Observation 结构化数据改名为 `HostValidatedPayload` 与 `HostSchemaRef`，明确
  已认证 Host 的校验信任边界；含糊旧名称被删除且不保留 Alias。
- 删除无效的 `AllowLegacySessionCreation` 选项与 `ValidateRequiredFields`
  兼容 Alias。所有接受 `context.Context` 的公共 Go API 都会在调用依赖或修改
  状态前拒绝 nil。

### 修复

- Event Replay 与 Transfer Import 现在会在 Reduce 前校验每个类型化 Payload；
  自洽哈希的恶意 Event 会返回损坏日志错误，不再解引用缺失的动作生命周期记录。
- Epoch 现在与外层 Session 绑定；Host Sequence 字段统一要求正数且 JSON-safe；
  Wire 与持久化 JSON 统一拒绝重复 Object Member Name。
- Archive/Delete Exact Retry 现在会重新 Fence 已可见 Marker，并完成 Rename
  之后遗留的 Deleting Directory。Transfer Publish 在父目录 Sync 失败后可确认
  或精确重试完整 Target。
- 不确定 Event Append 的 Exact Retry 会复用原 Storage Reservation，不再对可能
  已经存在的同一 Event 重复计费。
- HostKit Workflow State v2 会在效果前预检 Action/Outbox 容量，在权威线程校验
  可信 Principal Scope，验证本地结构化 Output，保留执行不确定结果，并压缩已确认
  的 Terminal Action。
- 修正当前文档中的 28 Route SDK 清单、Terminal Story 100 回合门禁、
  Windows/macOS Luanti 验证及 Linux/macOS Ren'Py 覆盖范围，并删除已移除的
  Recovery 示例命令。
- Health 能力协商现在把空的推荐 Feature Baseline 编码为 `[]` 而不是 `null`，
  与所有 SDK 的数组契约一致。
- Windows Luanti 生命周期门禁为冷启动保留有界时间，并把 Archive、源码 Host
  和生成 Host 的失败拆分为独立 CI Step。
- File Store 会原子 gzip 压缩可重建的 Checkpoint Projection；权威 Event Log
  仍保持普通 Hash-chained JSONL。
- 加速一年容量测试改为独立普通测试门禁；Race Build 保留并发/生命周期覆盖，
  不再重复执行会超过 Go 标准超时的磁盘容量负载。
- Luanti 生命周期校验同时接受精确版本输出与带构建信息的版本输出，并为每次
  Server 运行分配新的 UDP Port，使 Windows 与 macOS 官方发布包均不依赖默认端口；
  CI 失败会把转义后的 Server 诊断写入 Step Annotation。临时测试改用 Luanti
  世界内嵌 Game 布局，不再依赖平台不同的 User Game 搜索路径。
- 示例索引不再引用已删除的 Recovery 示例；相对文档链接测试会拒绝未跟踪的本地
  路径，避免开发工作区残留掩盖过期链接。
- `rin doctor host` 现在执行有界且限制输出的版本探测，会拒绝失效命令 Shim，
  并尝试兼容 Windows 的 Python 命令名。
- 非 Loopback 监听若未同时声明远程监听、Bearer 鉴权与 TLS Reverse Proxy
  终止，会在接触数据目录前失败；正式路径是同机 TLS Proxy 加 Loopback Rin。
- Terminal Story 存档变更现在使用跨进程磁盘 CAS，在 Lease 内刷新 Outbox；
  Lock Release 结果不确定时保留已经成功的持久提交，并对所有已存在 Lock
  Fail Closed，不再尝试存在竞态的 PID 自动恢复。

## [0.6.0] - 2026-07-24 - Preview

只有发布检查全部通过后，才会从已验证的主分支创建 `v0.6.0` Tag。流程见
[发布指南](docs/release-guide.zh-CN.md)。

### 新增

- JavaScript、C# 与 Java 新增带版本的宿主持久保证校验和共享 Pending Turn
  Workflow Coordinator。
- 固定版本、可安装的 Fabric 1.21.1 服务端 Mod，具备稳定 Saved Data 身份、
  可重启 Pending Turn/Outbox 状态及 Linux/Windows 构建。
- 由游戏掌握权威的 Observation -> Proposal -> Apply/Reject -> Commit 生命周期；
  `outcome-reporting-v1` 支持延迟 Outcome 合并和游戏侧持久 Outbox 恢复。
- 覆盖完整 lineage 的持久 Request/Event ID History，包括 exact retry 原始结果
  和 Store append 结果未决时的 fail-closed 恢复。
- 由 Feature 控制的 Memory Archive、Actor 本地冲突 Belief、候选 Goal、
  Actor Activity、世界仲裁和原子 Batch Outcome。
- Timeline、指定 Revision Replay、内部重放 Checkpoint、`rin inspect`，以及通过
  `Engine.VerifyAll()` 显式执行的全历史校验。
- 有界队列、保留、取消、Provider 重试与熔断保护的异步 Proposal 和结构化
  Generation Job。
- 源码优先的 Python、JavaScript、C#、Java、Lua Client，以及 Ren'Py、Godot、
  Unity、Fabric、BepInEx、Luanti 接入示例。
- 离线 `rin init host` 生成器，可创建引擎无关的 Go、JavaScript、Python、
  C#、Java、Lua 契约骨架，以及独立 Fabric、单 Backend BepInEx Mono/IL2CPP
  与 Luanti 项目。`rin add skill`、`rin conformance host` 与
  `rin doctor host` 分别生成密封能力、校验契约并诊断跨平台 Runtime。
- 类型化通用 HostKit 参考实现，明确 Transport、所属线程、状态、身份、
  Observation、Capability、执行与 Artifact 八个端口；Coordinator 在联网前
  持久化、校验精确 Offer、跟踪长动作、精确重试 Outbox 并对账陈旧 Epoch。
- 无依赖 C99 Host 参考与共享 Host Scenario Contract，在 GCC/Clang 和 MSVC
  上以 Warning-as-error 构建。
- Preview Unreal Runtime Plugin 骨架，包含显式持久 Epoch 绑定、Game Thread
  Capability 授权、World 切换的 `outcome-unknown` 处理，以及 Behavior Tree
  移动 ActionRun 示例。Linux/Windows CI 会检查其布局与受限执行入口；真实
  Unreal 构建仍是有文档记录的人工门禁。
- Rollback-aware Ren'Py Host Epoch Coordinator。Save Data 只保存普通 Epoch，
  Persistent Data 保存有界单调高水位；Load/Rollback 会 Fork Timeline，进程内
  Proposal Worker 会失效，迟到结果不能恢复旧分支。
- Integrated 与 Dedicated Server 共用的 Fabric Logical Server Runtime。真实
  Lifecycle Event 会绑定新的 Host/Timeline Generation，精确 Epoch 校验拒绝旧
  Server 工作，Shutdown 会关闭 Authority Dispatch，Build 会运行官方 Dedicated
  Server GameTest。
- Unity Authority Lifecycle：Domain/Scene Generation、持久化不透明 Offer
  参数与 Active Run、可取消长动作 Handle、旧 Callback 拒绝，以及游戏拥有的
  NavMesh 移动示例。
- Godot 4.6.3 Authority Lifecycle：持久 Host/World/Timeline Generation、
  完整 Offer Binding、Active Run 恢复，以及 Linux/Windows 官方 Headless
  生命周期测试。
- [`api/openapi.json`](api/openapi.json) 中的 OpenAPI 3.1 Wire Schema 与
  [兼容矩阵](docs/compatibility.zh-CN.md)。
- 有界 frame 的 NDJSON Session Transfer：immutable export boundary、逐 event
  与整流 checksum、可信 Binding header、同根 staging import、atomic publish、
  终止 error frame，以及调用方拥有的 JavaScript/C# stream helper。端到端测试
  会迁移超过 16 MiB 的 lineage、重放并继续 mutation。
- 经过鉴权的 Session Stats、Archive 与 Fail-closed Delete（永久 ID Tombstone），
  以及可配置的受管存储 Soft/Hard Quota。File Store 生命周期恢复在 Linux、
  macOS 与 Windows 上均有覆盖。
- 分离的 Liveness/Readiness Probe、经过鉴权的有界 Diagnostics、无依赖
  Prometheus Metrics，以及不含玩家内容的结构化 Request Correlation。
- 可安装 Node.js 终端故事纵向切片，包含持久 JavaScript 工作流、跨平台验收 Job、
  原始基准证据，以及同样持久化的规则树对照。

### 变化

- 新 Session 必须使用 `outcome-reporting-v1` 安全基线；未启用它的既有
  History 与 Exact Create Retry 继续保持历史 Reducer 与 Commit 语义。
- Restore 现在必须提供来自运行中游戏可信内容 Manifest 的
  `expected_binding`，并同时匹配导入 Snapshot 和任何已存在的目标 Session。
- `rin.reducer-projection/v2` 使用游戏编写的 Action Description 重建 Proposal
  展示文本，并采用公平的有界 Memory Summary 取样；它不会改写权威事件字节。
- 有界召回 Memory 的 Tag 现在会在 Goal 偏好之后影响确定性白名单 Action 选择，
  让离线记忆产生行为价值，同时不暴露私有 Memory 文本。
- 随附 File Store 改为 Lazy Load Session，使用 Revision Index 和派生
  Checkpoint，永久保留事件日志，并且只支持具有文档所述锁与同步语义的本地文件系统。

### 加固

- 宿主接入现在必须显式声明 `advisory`、`idempotent-action` 或
  `transactional-action` 持久保证 Profile。仓库记录只减不增的示例代码预算，防止
  协议流程逻辑继续静默膨胀到游戏 Adapter 中。
- Java 统一处理 Proposal Freshness 和终态 Commit 到安全 Observe 的恢复；
  Fabric 适配器仍如实保持 `advisory`，因为 Saved Data Dirty Mark 不是同步
  事务边界。
- Inline Snapshot compact JSON 上限为 16 MiB；默认请求正文和随附客户端响应正文
  上限为 32 MiB。超限状态会被拒绝，绝不截断。
- Snapshot 与 Checkpoint Hash 是 Checksum，不是签名或来源证明。Event Hash
  同样无密钥，不能阻止拥有写权限的一方重建完整事件链。
- Provider Prompt、凭据和原始 HTTP 正文不会进入错误、日志或持久 Session State；
  经验证的 Generation Content 在返回调用方前，会作为有界的进程内 Job/Cache
  数据暂存。
- 公共 HTTP JSON 整数使用可精确跨语言表示的
  `-9007199254740991` 至 `9007199254740991`；Schema 可继续施加更窄的非负约束。
- Commit 与 Batch Commit Item 的 `accepted` 必须显式出现；省略不解释为
  `false`。
- 游戏侧原始 HTTP 请求正文和成功 Provider JSON Response 都会在解码前严格检查；
  非法 UTF-8 与未配对 Unicode Surrogate 会被拒绝。非 2xx Provider Body 只用于
  有界错误分类，绝不会成为 Generation Content 或 Session State。

### 兼容说明

- Wire 标识仍为 `rin.protocol/v1`，但 Preview v1 已增加响应字段、Feature-gated
  语义和更严格的请求校验。Sidecar、Client 源码与 Conformance Inventory 应固定
  到同一仓库 Revision。
- 请求拒绝未知字段；客户端必须容忍响应中未知的增量字段。
- HTTP 失败使用错误 Envelope。Proposal 或 Generation Job 也可能以 HTTP `200`
  到达终态，并由 `data.error` 表示异步操作失败。
- SDK 仍采用源码优先分发，尚未发布到各语言 Registry。

### 已知限制

- Rin 仍为 Preview，不提供 post-1.0 级别的兼容或弃用保证。
- 完整 Inline Snapshot 仍不使用 streaming 且上限为 16 MiB；Session Transfer
  是完整 lineage 的受支持迁移/备份路径。
- 随附 File Store 支持本地 `darwin`、`linux` 与 `windows`，不支持网络、FUSE
  或云同步文件系统。
- Event 与 Snapshot Hash 不能认证遭到对抗性重写的历史。
- Fabric、BepInEx、Luanti 示例仍待在真实版本中完成人工安装与交互验收。

## 更早的实施里程碑

仓库历史中存在名为 0.1 至 0.5 的实施里程碑。它们是开发阶段，不表示对应公共
Release Tag 必然存在。已交付能力汇总在[路线图](ROADMAP.md)中。

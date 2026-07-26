# 变更日志

[简体中文](CHANGELOG.zh-CN.md) | [English](CHANGELOG.md)

本文记录仓库级变更。Rin `0.6.0` 是 Preview 版本：它仍处于 pre-1.0 阶段，
后续 minor 版本之间会记录兼容变化，但不承诺始终保持兼容。

## [0.6.0] - 2026-07-24 - Preview

只有发布检查全部通过后，才会从已验证的主分支创建 `v0.6.0` Tag。流程见
[发布指南](docs/release-guide.zh-CN.md)。

### 新增

- JavaScript、C# 与 Java 新增带版本的宿主能力校验和共享 Pending Turn
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
- 离线 `rin init mod` 生成器，可创建独立的 Fabric、单 Backend BepInEx
  Mono/IL2CPP 与 Luanti 项目，内置 SDK 源码、固定依赖、确定性文件 Hash、
  Windows 安全路径和禁止覆盖的输出语义。
- [`api/openapi.json`](api/openapi.json) 中的 OpenAPI 3.1 wire Schema、
  [兼容矩阵](docs/compatibility.zh-CN.md)和
  [v0.6 迁移指南](docs/migration-v0.6.zh-CN.md)。
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
  `transactional-action` 能力 Profile。仓库记录只减不增的示例代码预算，防止
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

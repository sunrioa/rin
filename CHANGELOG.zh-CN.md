# 变更日志

[简体中文](CHANGELOG.zh-CN.md) | [English](CHANGELOG.md)

当前源码为 `0.7.0` Preview。pre-1.0 期间允许破坏性修改；每次发布应固定准确
Commit 或 Tag。

## 未发布：Harness V2

### 新增

- `rin.host/v2`：Observation、CapabilitySpec、ActionRequest、BoundAction、Effect、
  ActionRun 和 ActionOutcome 的引擎无关契约。
- `rin.control/v2`：常驻 Control Daemon、Host Lease、Controller Lease、Emergency
  Stop、Action Gateway、Policy、Operation、确认、取消和结果对账。
- 可选 Internal Agent Runtime：Persona、Memory、Skill、结构化模型决策、异步
  Agent Task API 和 Macro 父子循环。
- MCP 2026-07-28 薄代理和 Codex、Claude Code、OpenClaw 的本机安装、状态、更新
  与卸载命令。
- Python、JavaScript、C#、Java、Lua Control V2 客户端和 Go HostKit。
- Grid、Story、Terminal 三个 V2 验证 Adapter。
- Go、JavaScript、Python、C#、Java、Lua 通用 Host 契约脚手架。
- 可选 OpenAI-compatible 语义记忆召回，具有显式 Domain 外发范围、可重建 SQLite
  向量投影和离线降级。

### 变化

- 内部模型请求改用确定性静态 Prompt 前缀，并把兼容供应商的缓存 Token 别名映射到
  现有任务时间线。
- 模型从选择 Host 预制少量选项，改为在已发布 Capability 和可信 Observation 内
  自行选择能力、参数和目标。
- 授权从能力名称判断改为 Host Binding 后按实际 Effect、所有权、Scope、风险、
  Rule 和 Budget 判断。
- 内部模型与外部 MCP 使用同一 Controller Lease、Action Gateway、Policy、
  Operation 和 Host Outcome 语义。
- MCP 进程改为无状态 STDIO 代理；端口、持久状态和固定 Principal 由常驻
  `rin-control` 拥有。
- Host 可让声明过的只读 Principal 在 Actor 暂时为空时继续发现世界，而不会授予
  控制或执行权限。
- SDK 收敛为 source-first Control V2 客户端；HTTP 精确契约由 OpenAPI 提供。
- Host 脚手架只生成 `custom` 契约骨架，不再声称生成真实引擎工程。
- `AgentRuntime` 的上下文装配、任务生命周期、计划决策、动作操作协调和 Signal 唤醒已拆为
  包内组件；拆分不改变模型、Planner、Operation 或任务时间线语义。

### 安全

- Controller 不能声明 Effect、所有权、风险、授权结果或执行成功。
- 内置安全内核拒绝任意代码、文件访问、原生调用、权限伪造、秘密泄露及未知
  Effect/Scope/Ownership。
- Control Daemon 只监听回环地址，要求至少 32 字节 Token；Agent API Token 与模型
  API Key 必须分离。
- Internal Agent 将 Persona、Memory、Skill、Observation 和玩家文本放入
  `untrusted_context`，模型输出使用封闭 JSON Schema 并复验允许集合。
- Operation 显式区分排队、接受、运行、成功、失败、过期和结果未知；只有 Host
  Outcome 能令 `execution_confirmed=true`。

### 删除

- 删除未被 V2 消费的旧运行时、规划 DSL、兼容分支和迁移工具。
- 删除复制过时协议的 Ren'Py、Godot、Unity、Unreal、Fabric、BepInEx、Luanti 与
  Native 示例，以及对应的工具链和伪 Conformance。
- 删除旧引擎模板；具体游戏接入由独立 Adapter 仓库负责。
- 删除只描述已移除架构、迁移和重复流程的文档。
- 删除旧文件记忆后端和 `memory.json` 首次迁移路径；`memory.db` 是 Rin Memory 域唯一在线
  存储，JSONL 仅用于显式交换。

### 当前验收状态

- Rin Go 核心、Race、OpenAPI、五语言 SDK 和三个 V2 示例具备自动化门禁。
- Minecraft 与视觉小说两个真实 Adapter 的自动契约与跨进程回归已通过；安装、存读档、
  强制终止、多人权限、急停、UI、长时间游玩和角色自然度仍需人工验收。

## [0.6.0] - 2026-07-24 - Preview

本节记录 `v0.6.0` Tag 当时的行为。它描述的是已经退役的 V1 架构，仅作为发布历史
保留，不是当前 V2 的使用文档。

### 新增

- 由游戏掌握权威的 Observation -> Proposal -> Apply/Reject -> Commit 生命周期，
  包括延迟 Outcome 合并和游戏侧持久 Outbox 恢复。
- 覆盖完整 Lineage 的持久 Request/Event ID History、Exact Retry 结果、指定 Revision
  Replay、内部重放 Checkpoint、`rin inspect` 和显式全历史校验。
- 由 Feature 控制的 Memory Archive、Actor 本地 Belief 与 Goal、Actor Activity、
  世界仲裁和原子 Batch Outcome。
- 具有有界队列、保留、取消、Provider 重试和熔断的异步 Proposal 与结构化
  Generation Job。
- 源码优先的 Python、JavaScript、C#、Java、Lua Client、一份 OpenAPI 3.1 Wire
  Schema，以及该版本发布时提供的引擎接入示例。

### 变化

- 新 Session 可启用延迟 Outcome 上报；既有 Session 保持原有 Reducer 与 Commit
  语义。
- Restore 要求提供运行中游戏可信内容 Manifest 的 `expected_binding`，并同时核对
  导入 Snapshot 与已存在的目标 Session。
- `rin.reducer-projection/v2` 可重建 Proposal 展示内容，同时不改写权威事件字节。
- 随附 File Store 增加 Session Lazy Load、Revision Index 和派生 Checkpoint，并继续
  永久保留事件日志。

### 安全

- Inline Snapshot JSON 上限为 16 MiB；默认请求正文和随附 Client 响应正文上限为
  32 MiB。超限输入会被拒绝，而不是截断。
- Provider Prompt、凭据和原始 HTTP 正文不会进入错误、日志或持久 Session State。
- 公共 HTTP JSON 整数使用可精确跨语言表示的范围；Commit 接受结果要求显式字段；
  游戏侧请求及成功 Provider JSON 中的非法 UTF-8 或 Unicode 会在解码前被拒绝。
- Snapshot、Checkpoint 与 Event Hash 被明确视为无密钥 Checksum，而不是签名或对抗
  历史重写的证明。

### 兼容说明

- 这是 pre-1.0 Preview 契约。分发时需要把 Sidecar、Client 源码和 Conformance
  Inventory 固定到同一仓库 Revision。
- 请求拒绝未知字段，Client 则应容忍响应中的增量字段。SDK 采用源码优先分发，未发布
  到各语言 Registry。
- 完整 Snapshot 没有流式传输；随附 File Store 仅支持 `darwin` 与 `linux` 的本地
  文件系统。

## 更早的实施里程碑

仓库历史中还存在名为 0.1 至 0.5 的实施里程碑。它们是开发阶段，不表示存在对应的
公共 Release Tag。

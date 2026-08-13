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
- SDK 收敛为 source-first Control V2 客户端；HTTP 精确契约由 OpenAPI 提供。
- Host 脚手架只生成 `custom` 契约骨架，不再声称生成真实引擎工程。

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

### 当前验收状态

- Rin Go 核心、Race、OpenAPI、五语言 SDK 和三个 V2 示例具备自动化门禁。
- 真实游戏 Adapter 仍必须完成安装、存读档、强制终止、多人权限、急停、UI、
  长时间游玩和角色自然度的人工验收。

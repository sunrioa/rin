# 可选决策、记忆、语音与遥测端口

[English](optional-extensions.md) | [简体中文]

Rin 的权威核心不依赖模型供应商、Agent 框架、向量数据库、语音服务或遥测后端。
集成通过小型 Go 端口实现，并始终位于游戏权威之外。

## 端口边界

| 端口 | 输入与输出 | 权威规则 |
| --- | --- | --- |
| `runtime.DecisionProvider` | 不可变决策上下文 -> 结构化 `DecisionDraft` | 只能选择当前由游戏编写的 Offer |
| `provider.StructuredGenerationProvider` | Message 与 JSON Schema -> 不可信结构化 Content | 不能修改 Session State 或执行动作 |
| `extension.MemoryIndex` | 带来源的派生文档 -> 有界 Document ID 与分数 | Event History 仍是权威记录 |
| `extension.SpeechProvider` | 已批准展示的文字 -> 不可变 `AudioArtifactRef` | 不能授予或执行游戏权威 |
| `extension.TelemetrySink` | 固定且不含内容的生命周期字段 | 遥测失败不得改变游戏结果 |

实现必须并发安全并配合 `context.Context` 取消。供应商 SDK 的请求/响应类型必须在
Adapter 边缘转换，不能泄漏到这些契约。

## 决策与结构化生成

`DecisionDraft` 只含结构化 ID、stance 与审计关联，刻意不提供自由文本 Summary
或 Rationale。运行时会验证每个返回 ID，并只从被选中、明确允许展示的
`ActionOffer.description` 与固定 stance 模板构造玩家可见 Proposal 文本。

`StructuredGenerationProvider` 是更底层的内容端口。它的输出是不可信数据，不是
命令。游戏必须校验封闭 Schema，并通过自己的权威规则后才能使用。

MCP、A2A、LangGraph、Microsoft Agent Framework、OpenAI Agents SDK 或其他
Agent Runtime 可以作为这些端口后的 Adapter，但不是 Rin Core 依赖；其中的
Tool Call 不会自动成为 Host Capability。

## 派生长期记忆

`MemoryIndex` 是可丢弃的搜索投影，不是 Canon。每个 `MemoryDocument`：

- 只属于一个 Session 与 Actor；
- 带有一个或多个权威 Source Event ID；
- 带有由框架计算的文本 SHA-256 绑定；
- Text、Tag、Source 数量与 Tick 范围均有上限。

使用 `RebuildMemoryIndex` 原子替换一个 Session 的完整投影，
`SearchMemory` 校验有界结果，`DeleteMemoryIndex` 执行隐私删除。Adapter 收到
克隆输入，调用方也收到克隆结果。索引丢失或损坏时应从受保护的 Event History
重建；不得从向量检索结果反向构造权威 Event。

## 语音

`SpeechManager` 只合成已经获准展示的文字。请求绑定 Session、Actor、Operation、
Language、Voice、规范 Audio Media Type，以及由框架计算的文本 Hash。Provider
返回不可变外部 Artifact 的 Metadata；原始音频不会进入 Rin Session State 或
Telemetry。

Manager 提供：

- 按 Session 与 Actor 隔离的有界 TTL/LRU Cache；
- 协作取消；
- Provider 失败或 Artifact 非法时的纯文字降级；
- 固定字段的合成遥测；
- 由游戏显式提交的播放回报。

Host 决定是否以及如何在引擎线程播放音频。例如 Ren'Py Adapter 在 Worker 中准备
语音，再通过 `renpy.invoke_in_main_thread` 调度返回的 Artifact；存档只保存普通
关联状态，不保存 Thread、Callback、Provider Client 或原始音频。Unity、Godot、
Unreal、浏览器和 Mod Host 使用各自原生 Audio Presenter，但遵循同一模式。

Speech Provider 必须保证返回的 Artifact 在配置的 Cache 生命周期内可读取。
关联 ID 必须是不透明 ID，不得编码对话、凭据、Prompt 或个人数据。

## 遥测与隐私

`TelemetryEvent` 是封闭字段集合：事件名、不透明关联 ID、状态、耗时和时间戳。
它没有任意 Attribute、对话、Prompt、音频、凭据或存档 Payload 字段。合成遥测
是 Best-effort，不能把有效文字变成失败决策。显式 Playback Report 会返回 Sink
错误，以便有需要的 Host 用自己的 Observability Outbox 重试。

## Computer Control 属于不同信任等级

读屏或输入模拟集成不是 Semantic Host Adapter，无法提供同等级别的精确对象身份、
Offer Binding、主线程权威或 Outcome 证明。未来若提供 `ComputerControlHost`，
应放在独立进程与权限配置中，要求用户明确选择，并审查目标游戏 EULA 与反作弊规则。
它的 Observation 与建议输入仍不可信，也不得宣传为正常 Host Conformance。

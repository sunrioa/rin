# Roadmap

## v0.1.0 - Runtime foundation

- [x] Go 标准库 HTTP Sidecar
- [x] 多角色 Session、Observation、Memory、Belief、Goal
- [x] 角色边界与候选动作白名单
- [x] propose / commit 世界权威分离
- [x] tick 调度与紧急提案
- [x] 幂等 request ID、revision 与过期提案
- [x] 哈希链 JSONL、原子 Snapshot、Restore
- [x] 确定性离线 Policy
- [x] macOS / Windows / Linux CI 与零 CGO 构建

## v0.2.0 - Optional model policy

- [x] 标准库 OpenAI-compatible HTTP Provider
- [x] Provider 超时、取消、重试预算和熔断
- [x] 严格结构化 Draft 与 prompt injection 数据隔离
- [x] 异步预取 Job API；游戏主线程永不等待模型
- [x] 基于 head hash 的不可变提案缓存
- [x] Provider contract fixtures，不提交真实 API Key

## v0.3.0 - Game adapters

- [x] Ren'Py Python 客户端与离线回退
- [x] Godot GDScript 示例
- [x] Unity C# 示例
- [x] RPG 区域、可见性和任务事件约定
- [x] 当前 `ai-galgame` 的协议兼容测试向量

## v0.4.0 - Structured generation integration

- [x] 通用异步结构化 Generation Job API
- [x] 请求幂等、语义缓存、取消、输出大小和 JSON Object 校验
- [x] Ren'Py Generation 客户端与 `ai-galgame` 全链路接入
- [x] 游戏供应商凭据迁移到独立 Sidecar
- [x] Observation / Proposal / Commit / Snapshot 与剧情生成组合流程

## v0.5.0 - Living worlds

- [ ] 分层记忆总结与可解释遗忘
- [ ] 角色私有认知、传闻来源和事实冲突
- [ ] 自主小目标与 Game Master 仲裁
- [ ] 多 Agent 批处理与区域休眠
- [ ] 人工调试时间线和决定回放工具

详细协议、兼容策略、阶段提交与验收矩阵见
[`docs/living-worlds-v0.5-plan.md`](docs/living-worlds-v0.5-plan.md)。

每个阶段继续保持一个原则：模型可以提出意图和表达，游戏引擎决定现实发生了什么。

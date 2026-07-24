# 路线图

[简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

**当前状态：** Rin `0.6.0` 是 Preview、pre-1.0 软件。下列编号是已经交付的实施
里程碑，不表示每个编号都存在公共 Tag。只有
[发布清单](docs/release-guide.zh-CN.md)通过后，才会创建已验证的 `v0.6.0` Tag。

路线图记录可复用的 Runtime 能力，不把某个游戏的接入进度纳入公共 Runtime
定义；未勾选项不属于受支持能力。

## 里程碑 0.1 - Runtime 基础

- [x] Go 标准库 HTTP Sidecar
- [x] 多角色 Session、Observation、Memory、Belief 与 Goal
- [x] 角色 Boundary 和 Candidate Action Allowlist
- [x] Proposal/Commit 世界权威分离
- [x] Tick 调度与 Urgent Proposal
- [x] Request ID、Revision、过期 Proposal 保护与确定性 Policy
- [x] Hash-chained JSONL、Snapshot、Restore 与确定性 Replay
- [x] macOS、Windows 与 Linux Build Job

## 里程碑 0.2 - 可选模型 Policy

- [x] Go 标准库 OpenAI-compatible HTTP Provider
- [x] Attempt/Total Timeout、协作取消、有界重试与 Circuit Breaker
- [x] 严格结构化 Draft 与 Prompt/游戏数据隔离
- [x] 异步 Proposal Job 与按 Head 建 Key 的不可变 Draft Cache
- [x] 不含真实 API Key 的 Provider Fixture

## 里程碑 0.3 - 游戏 Adapter

- [x] Ren'Py Python Client 与 Authored Offline Fallback
- [x] 保持引擎线程权威的 Godot 4 与 Unity 示例
- [x] RPG Region、Visibility 与 Quest Event 约定
- [x] 可执行协议兼容 Fixture

## 里程碑 0.4 - 结构化生成

- [x] 通用异步结构化 Generation Job
- [x] 有界 Request Identity、Semantic Cache、取消、输出大小与 JSON Object 校验
- [x] Ren'Py Generation Client 与参考组合流程
- [x] Provider 凭据只保留在独立 Sidecar
- [x] Generation 不进入 Session 世界权威或 Canon

## 里程碑 0.5 - Living World 基础

- [x] Feature-gated 分层 Memory Summary 与可解释遗忘
- [x] Actor 私有知识、带来源冲突 Claim 与有界 Belief 选择
- [x] 游戏提供的 Candidate Goal、Actor Activity 与区域 Dormancy
- [x] 确定性建议仲裁与原子多 Actor Outcome 记账
- [x] 脱敏 Timeline、指定 Revision Replay 与 `rin inspect`

## 里程碑 0.6 - Preview 接入与加固

- [x] 源码优先的 Python 3.9+、JavaScript/Node 18+、.NET 6+、Java 17+、Lua 5.1+ Client
- [x] 统一 20 Route OpenAPI 3.1 Wire Schema 与生成的 SDK Route Inventory
- [x] Fabric、BepInEx 6 与 Loopback-only Luanti 示例 Mod
- [x] 游戏权威 `outcome-reporting-v1`、Proposal Attempt 与 Outcome Outbox 语义
- [x] 永久 Request/Event ID History 与 Fail-closed 未决 Append 对账
- [x] 可信 Restore Binding、Snapshot 大小限制与明确 Checksum Trust Boundary
- [x] Lazy Session 恢复、Range Read、派生 Checkpoint 与全历史运维审计
- [x] 玩家文本重建与公平有界 Memory Summary Projection
- [x] 双语 Changelog、兼容矩阵、迁移指南与发布清单
- [ ] 在真实 Fabric、BepInEx、Luanti 游戏版本中完成人工安装与交互验收

## Preview 发布门禁

发布 Preview Tag 前：

- [ ] 发布 Commit 通过必要的 Go、Adapter、SDK、契约生成和跨平台 Build 检查
- [ ] OpenAPI、生成 Route Inventory、Protocol 文字与两套语言文档不存在漂移
- [ ] Fresh Clone 能 Checkout、测试并构建候选 Tag

这些门禁描述发布 Commit 必须验证的工作；本文不宣称已有语言 Registry Package、
自动 Binary Pipeline、Streaming Snapshot Transport、密码学签名或 post-1.0
稳定性。

每个里程碑都保持同一原则：模型可以提出意图和表达，游戏引擎决定现实发生什么。

# 路线图

[简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

**当前状态：** Rin `0.7.0` 是 Preview、pre-1.0 软件。下列编号是已经交付的实施
里程碑，不表示每个编号都存在公共 Tag。只有
[发布清单](docs/release-guide.zh-CN.md)通过后，才会创建已验证的 `v0.7.0` Tag。

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

- [x] Ren'Py Python Client 与 Fail-closed Proposal Recovery
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
- [x] 离线、确定性的 `rin init host` 契约骨架，覆盖六种自定义 Runtime、
  Fabric、单 Backend BepInEx Mono/IL2CPP、Luanti，以及密封能力生成、
  Conformance、Doctor 与 Windows 门禁
- [x] 游戏权威的类型化动作生命周期、Proposal Attempt 与 Outcome Outbox
- [x] 通用 HostKit 端口与 Coordinator，覆盖长时间 ActionRun 和 Epoch 对账
- [x] 可移植 C99 Host 参考与跨引擎共享 Scenario Contract
- [x] Preview Unreal Runtime Plugin 骨架，覆盖显式 Epoch 绑定、Game Thread
  最终授权与 Behavior Tree 长动作回报
- [x] Ren'Py Rollback-aware Epoch：Persistent 高水位、Load/Rollback Timeline
  Fork 与旧 Worker 失效
- [x] Fabric Integrated/Dedicated Logical Server Authority、Lifecycle Epoch、
  旧工作拒绝与官方 Dedicated Server GameTest
- [x] Unity Domain/Scene Authority、持久 Active Run、可取消 NavMesh 长动作与
  迟到 Callback 拒绝
- [x] 永久 Request/Event ID History 与 Fail-closed 未决 Append 对账
- [x] 可信 Restore Binding、Snapshot 大小限制与明确 Checksum Trust Boundary
- [x] Lazy Session 恢复、Range Read、派生 Checkpoint 与全历史运维审计
- [x] 玩家文本重建与公平有界 Memory Summary Projection
- [x] 双语 Changelog、兼容矩阵、迁移指南与发布清单
- [x] 可安装 Node.js 可玩切片、持久化规则树对照、原始基准证据，以及
  Windows/macOS/Linux 验收 Job
- [ ] 在真实 Fabric、BepInEx、Luanti 游戏版本中完成人工安装与交互验收

## 里程碑 0.7 - 通用 Host 基础

- [x] 引擎无关 Go `host` Contract，覆盖宿主 Manifest、Epoch、对象引用、
  Capability Descriptor、Offer、Invocation、ActionRun 与 Outcome
- [x] 自包含 JSON Schema 2020-12 参数/结果校验与确定性 Descriptor Digest
- [x] 并发安全 Capability Registry、精确版本、动态撤销和执行前 TOCTOU 复验
- [x] 明确区分 Capability Discovery、每轮游戏授权、执行生命周期和持久保证
- [x] 跨 SDK 将旧 `HostCapabilities` 清理为准确的 `HostDurability`，不保留别名
- [x] Schema Fuzz、Registry Race、过期 Epoch/Digest/撤销和状态转换测试
- [x] 将 Host Contract 接入跨语言 SDK、通用脚手架，以及 C99/Unreal
  Reference Adapter；其余真实宿主验收继续按证据矩阵推进

## Preview 发布门禁

发布 Preview Tag 前：

- [ ] 发布 Commit 通过必要的 Go、Adapter、SDK、契约生成和跨平台 Build 检查
- [ ] OpenAPI、生成 Route Inventory、Protocol 文字与两套语言文档不存在漂移
- [ ] Fresh Clone 能 Checkout、测试并构建候选 Tag
- [ ] 玩家价值主张不超出实测范围，并通过
  [证据门禁](docs/player-value.zh-CN.md)

这些门禁描述发布 Commit 必须验证的工作；本文不宣称已有语言 Registry Package、
自动 Binary Pipeline、密码学签名或 post-1.0 稳定性。Inline Snapshot 仍不使用
streaming；有界 Session Transfer 是独立的完整 lineage 受支持路径。

## 下一阶段优先修复

- [x] 按[可扩展 Session Transfer 设计](docs/session-transfer.zh-CN.md)实现有界内存、
  可验证、原子发布的完整 lineage 导出与导入，解除 Identifier History 增长后
  Snapshot、Replay 与 Restore 全部不可用的生命周期硬上限。
- [x] 在完成超过 16 MiB 的端到端、取消、损坏和崩溃恢复测试前，不把该能力标记为
  已支持，也不以单纯提高请求正文上限代替流式传输。
- [x] 实现 Windows 数据目录独占锁与真实 Sidecar 持久化/重启/锁竞争测试；
  Windows 支持是项目约束，交叉编译成功不能代替运行时支持。
- [x] 从发布价值主张中移除未经测量的 Optional Cognition Feature；单偏好切片
  只与小得多的持久化规则树持平，不支持更宽泛的“值得复杂度”宣称。

每个里程碑都保持同一原则：模型可以提出意图和表达，游戏引擎决定现实发生什么。

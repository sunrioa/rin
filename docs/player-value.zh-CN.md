# 玩家价值证据与发布门禁

[English](player-value.md) | [简体中文](player-value.zh-CN.md)

## 验证对象

受支持 Runtime 是可安装的 Node.js 18+ 终端故事
[`Last Station`](../examples/terminal-story/README.zh-CN.md)，它通过优先
JavaScript SDK 连接真实 File Store Sidecar。玩家告诉 Mira 饮品偏好；时间跳转后，
有界记忆召回会影响游戏编写的白名单 Action。对照组是持久化确定性规则树，不是
故意失忆的基线。

仓库中的 100 回合数据来自 darwin/arm64、Node 22.23.1 和确定性 Policy。它用于
复现，不是通用性能承诺。原始证据见
[`benchmark-darwin-arm64.json`](../examples/terminal-story/evidence/benchmark-darwin-arm64.json)。

| 指标 | Rin 安全回合 | 持久化规则树 |
| --- | ---: | ---: |
| 接入非空代码行 | 92 | 19 |
| P50 | 62.88 ms | 9.99 ms |
| P95 | 72.06 ms | 11.12 ms |
| 玩家可见选择 | 茶 | 茶 |
| Provider 调用 / 成本 | 0 / USD 0 | 0 / USD 0 |

基准在每回合之间都会重载游戏存档。Sidecar Ready 耗时 40.81 ms。100 回合后
Rin 保留 604,954 Bytes；按每小时 60 回合做保守线性投影，100 小时为
36,297,240 Bytes。仅启动阶段的 Fallback 耗时 9.63 ms，仍显示游戏编写的茶
选项。Rin 变更开始后的失败不会回退，因为没有
响应不能证明操作没有发生。

## 坦诚结论

Rin 现在产生了可观察的离线记忆行为：召回的 `preference.tea` Tag 会选择
`offer.tea`，同时不泄露私有记忆文本。它还提供通用历史、审计、有界召回、
Exact Retry 和 Crash-safe Outcome Reconciliation。

但对单个偏好而言，Rin **没有**胜过专用持久化规则树。相同玩家可见结果多用了
73 行接入代码，P95 延迟约为 6.5 倍。因此，这条小规则不值得引入 Rin。只有当游戏
拥有足够多的独立记忆、Actor 与编写动作，使专用持久化和分支不再更便宜时，价值
假设才成立。

## 范围纠偏

当前玩家价值证据只覆盖强制 Outcome-reporting Transaction 与具有行为影响的有界
召回。Memory Archive、Belief Conflict、Candidate Goal、Actor Activity、
Arbitration、Structured Generation 与在线模型质量都从发布价值主张中移除；它们
保留为显式 Preview 兼容/实验能力，而不是推荐默认值。没有宣称付费 Provider 的
价值或质量；确定性 Provider 成本准确为零。

## 发布门禁

只有满足以下条件，Release 才能保留当前狭窄主张：

1. 终端切片在 Windows、macOS、Linux 均能安装并通过测试；
2. 真实 Sidecar 基准仍能召回编写偏好、报告零确定性 Provider 调用，并在变更开始
   后 Fail Closed；
3. 在文档 Workload 下，100 小时受管存储投影低于 50 MiB，同时 Operator 可设置
   更低 Hard Quota；
4. 至少一台有记录的参考机器上，确定性本地 P95 低于 100 ms；
5. 每条公开价值陈述都链接到带日期的原始证据。

更广泛的“值得复杂度”或 Stable 主张还需要独立的多领域切片，以及与同样持久化的
编写基线进行预注册、盲测。至少 20 名玩家、过半偏好 Rin 条件、连续性错误不增加，
并实测 Provider 支出。满足前，Optional Cognition Feature 不得进入推荐 Preset。

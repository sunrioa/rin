# Rin 0.7 兼容矩阵

[English](compatibility.md) | [简体中文]

Rin `0.7.0` 是 **Preview**、pre-1.0 软件。应固定精确 Commit 或已验证
`v0.7.0` Tag。Protocol v2 有意移除了 v1 Wire 与兼容 Reducer；评估当前开发版本
时应使用新数据目录，或显式 Export/Import。

## 权威来源

| 关注点 | 权威来源 |
| --- | --- |
| Path、Method、Status、JSON Shape | [`api/openapi.json`](../api/openapi.json) |
| Host、动作与恢复语义 | [协议 v2](protocol-v2.zh-CN.md)、[Host Contract](host-contract.zh-CN.md)、[动作生命周期](action-lifecycle.zh-CN.md) |
| SDK Operation Inventory | [`sdk/conformance/routes.json`](../sdk/conformance/routes.json) |
| 发布流程 | [发布指南](release-guide.zh-CN.md) |

若 Prose 与 OpenAPI 的 Wire Shape 冲突，以 OpenAPI 为准，Prose 属于文档缺陷。

## 支持 Surface

| Surface | 0.7 契约 | Consumer 规则 |
| --- | --- | --- |
| Wire | `rin.protocol/v2` | 发送精确值；v1 会被拒绝 |
| Route | `/v2/*` | 无 Alias Route |
| Request | Closed Object | 未知字段会被拒绝 |
| Response | Preview 期间可增量扩展 | 忽略未知响应字段 |
| Integer | JSON-safe Range | 不发送引号整数或 `BigInt` |
| 动作输入 | 完整 `ActionOffer` | 模型只能选择 `offer_id` |
| 动作结果 | 类型化 Decision/Invocation/Run/Outcome | Host 执行后才回报终态效果 |
| Epoch/Time | Observation 与 Offer 必需 | 不得用 Render Frame 代替 |
| Restore | 可信 `expected_binding` | Snapshot 作为不透明敏感数据 |
| Transfer | 有界 NDJSON | 超过 Inline Snapshot 限制时使用 |
| File Store | 本地可靠文件系统 | HA/Shared Storage 使用协调 Store |
| SDK | Source-first | Vendor 完整目录并固定 Rin Revision |
| Host | 按清单在 macOS/Linux/Windows Build/Test | 真实 Engine/Server 验收另行完成 |

## 可选 Feature

v2 Host 生命周期是基础协议，没有 Feature Flag。当前可选 Session Feature：

- `memory-archive-v1`；
- `belief-conflicts-v1`；
- `goal-candidates-v1`；
- `actor-activity-v1`；
- `arbitration-v1`；
- `identifier-history-v1`。

通过 `/health` 协商，并且只启用游戏真正持久化与实现的 Feature。

## 平台矩阵

| Component | macOS | Linux | Windows |
| --- | --- | --- | --- |
| Go Runtime/CLI | 已测试 | CI | CI |
| Python/Ren'Py Adapter 逻辑 | 已测试 | CI | CI |
| JavaScript SDK | 已测试 | CI | CI |
| C# SDK/Unity Compile Harness | 已测试 | CI | CI |
| Unreal Runtime Plugin 静态契约 | 未安装 | CI | CI |
| OpenSpiel 2.0.1 决策语义 | 已测试 | CI | CI |
| Java SDK/Fabric Compile | 已测试 | CI | CI |
| Lua SDK/Luanti 5.16.1 Dedicated 生命周期 | 已测试 | Lua CI | 真实 Server CI |
| 真实 Fabric/BepInEx/Unity/Unreal Host | 需手工证据 | 需手工证据 | 需手工证据 |
| Luanti 实时 Sidecar/多人/故障注入 | 需手工证据 | 需手工证据 | 需手工证据 |

编译与模拟引擎 API 是有效契约证据，但不能证明真实游戏内 Loader Compatibility、
Main Thread、Save Integration 或 Long Soak。

## Hash 与安全边界

Event Hash、Snapshot Checksum 与 Checkpoint Checksum 能检测状态不一致或意外
损坏，但不是 Signature、MAC 或 Provenance。应通过外部访问控制保护 Data
Directory、游戏存档、Export 与 Backup。

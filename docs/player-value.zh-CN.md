# 参考价值与证据

[English](player-value.md) | [简体中文](player-value.zh-CN.md)

Rin `0.7.0` 仍是 Preview 软件。仓库内示例只能证明契约行为，不能证明 AI 控制角色
一定比游戏专用规则更有趣、更便宜或更快。

## 已自动验证

引擎无关的 Grid 与 Story Adapter 会通过同一套 V2 HostKit、Effect Policy、控制权
租约、Operation 生命周期和权威 Outcome 执行：

```bash
go test ./examples/adapters/grid ./examples/adapters/story
```

Story 测试会让内部 Agent Runtime 与外部 MCP Client 操作同一个权威场景，并验证
过期状态拒绝、幂等重放、取消、重启隔离，以及由 Policy 拒绝的角色边界。

## 不作出的结论

- 不再把仓库内微基准直接解释为玩家价值。
- 参考故事不是可用性或剧情质量研究。
- 内存参考 Adapter 不能证明具体引擎的线程、存档或崩溃恢复能力。
- 模型可以增加表达变化，但小型固定行为仍可能更适合确定性规则。

稳定发布前，应在每个目标游戏中测量完整玩家流程：任务完成率、错误行动、人工干预
率、角色一致性感受、延迟、模型成本、存档增长，以及进程或网络故障后的恢复结果。
除非方法和负载已经版本化且可复现，否则机器相关的 Benchmark Artifact 应留在源码
仓库之外。

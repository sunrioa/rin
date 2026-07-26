# OpenSpiel 决策模型验证

[English](open-spiel-validation.md) |
[简体中文](open-spiel-validation.zh-CN.md)

Rin Runtime 不依赖 OpenSpiel。仓库把真实 OpenSpiel `2.0.1` Python Binding
作为游戏语义测试 Oracle，用来发现只按 NPC 顺序对话设计时很容易引入的错误。

## 已验证映射

| OpenSpiel State | Rin Host 投影 |
| --- | --- |
| 单个活动玩家 | `DecisionWindow.mode = sequential`，且只包含该 Actor |
| Simultaneous Node | 一个 `simultaneous` Window；每个 Actor 收集一个合法选择，再应用一个 Joint Action |
| Chance Node | 游戏拥有的 Transition；不创建 Decision Window，也不产生模型可选 Offer |
| Information State | Actor 私有 Observation Payload；绝不使用完整隐藏 State |
| `move_number()` | `step` Clock 输入；不是 Render Frame 或墙钟 |

投影只根据 `legal_actions(player)` 创建 Offer。Action Integer 被绑定在宿主编写的
Argument Object 中，Policy 不能发明集合外动作。应用 Proposal 时会重新检查完整
Decision Window 与当前合法动作集合。

## 可执行场景

[`tools/verify_open_spiel.py`](../tools/verify_open_spiel.py) 会运行：

- Tic-Tac-Toe：验证顺序所有权与旧 Decision Window 拒绝；
- Rock-Paper-Scissors：验证同时收集与 Joint Action 原子应用；
- Kuhn Poker：验证显式 Chance Transition 与不完全信息。

Kuhn 测试会创建两个 World：Player 0 的私牌相同，对手私牌不同。完整 State
不同，但 Player 0 收到的 Observation 与合法 Offer 相同；持有该牌的 Player
仍能看到不同 Information State。这是可执行的 Noninterference 检查，不是
命名或源码 Marker 声明。

CI 使用 `--no-deps --require-hashes` 和
[`tools/open_spiel_requirements.txt`](../tools/open_spiel_requirements.txt)
安装 OpenSpiel。该文件固定 `2.0.1` 发布的全部 CPython 3.11–3.14 macOS arm64、
Linux x86-64/aarch64 与 Windows x86-64 Wheel Hash。测试会在 macOS、Linux
与 Windows 运行。

## 未作出的承诺

OpenSpiel 是语义 Oracle，不是 Rin 生产 Adapter。该 Harness 不能证明游戏线程
Dispatch、存档持久性、Sidecar 恢复、视觉控制或长时间世界动作。Chance 概率仍
由游戏拥有；不能因为模型 Provider 能接收大 Prompt 就发送隐藏 State。

主要参考：

- [OpenSpiel Concepts](https://openspiel.readthedocs.io/en/latest/concepts.html)
- [OpenSpiel State API](https://openspiel.readthedocs.io/en/latest/api_reference.html)
- [OpenSpiel Installation](https://openspiel.readthedocs.io/en/latest/install.html)

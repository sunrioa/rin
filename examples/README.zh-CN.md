# Rin 示例

[English](README.md) | [简体中文](README.zh-CN.md)

请从 [`basic`](basic/) 开始。它刻意保持简短，只演示如何针对运行中的 Sidecar
创建 Session 并记录一次 Observe。其 ID 只存在于当前进程，因此它是开发 Smoke
Test，不是生产存档架构。

设计真实接入时请使用 [`recovery`](recovery/)。它演示稳定身份、持久 Proposal
Attempt、Applied-operation Marker、权威 Outcome Outbox、Exact Retry、离线对账、
Snapshot Binding、原子文件替换与重启恢复。这些复杂内容被独立放置，避免
Quickstart 再次变得不可读。

可安装的 Node.js 18+ [`terminal-story`](terminal-story/) 是覆盖
Windows/macOS/Linux 的可玩纵向切片，包含安全 JavaScript SDK 工作流、可复现
Sidecar 基准，以及不回避结果的持久化规则树对照。

各引擎与 Mod 目录演示宿主特有的线程和打包方式；其中的持久化 Hook 仍需接入
游戏自己的权威存档系统。

# Rin 总体流程图

本文用一张图说明 Rin 运行时决策、模型调用、Host 执行以及 MCP 外部控制如何协作。
Rin 是引擎无关的控制层，游戏 Host 始终是世界状态和世界修改的唯一权威。

```mermaid
flowchart TB
    subgraph ENTRY["调用入口"]
        GAME["游戏引擎 / Mod / Adapter"]
        EMBED["Go 程序直接嵌入 Rin"]
        AGENT["Codex / Claude Code / OpenClaw / 其他 MCP Client"]
    end

    subgraph HOST["游戏 Host：世界权威"]
        CAPTURE["捕获权威 Observation、Epoch、Timepoint"]
        OFFER["生成完整绑定的 ActionOffer"]
        SDK["HostKit / 跨语言 SDK<br/>保存 Pending Turn、Operation、Outcome Outbox"]
        EXECUTOR["游戏统一执行器<br/>在权威线程最终复核并执行"]
        WORLD["游戏世界<br/>渲染、物理、导航、战斗、背包、任务与存档"]
    end

    subgraph RUNTIME["Rin Runtime：决策与角色状态"]
        API["Runtime HTTP / Protocol v2<br/>默认端口 7374"]
        ENGINE["runtime.Engine<br/>确定性 Session 状态机"]
        MEMORY["Memory、Goal、Boundary、Belief<br/>事件日志、快照与身份历史"]
        JOB["异步 Proposal Job<br/>有界队列、取消与 stale 检查"]
        POLICY["决策策略"]
        LOCAL["确定性本地 Policy<br/>离线可用"]
        PROVIDER["韧性 Provider<br/>超时、重试、熔断、并发合并与缓存"]
        MODEL["OpenAI 兼容模型<br/>可选、不受信任"]
        VALIDATE["Schema、白名单、边界、新鲜度、Epoch 校验"]
        PROPOSAL["ActionProposal<br/>只能选择一个 Host Offer"]
        STORE["Store<br/>哈希链事件、Snapshot、Checkpoint"]
    end

    subgraph CONTROL["MCP Host Control：外部控制"]
        MCP["rin-mcp<br/>无状态 STDIO 薄代理"]
        DAEMON["rin-control<br/>常驻 daemon，默认端口 7375"]
        AUTH["Token、Principal、Scope、Actor 控制权"]
        READMODEL["World / Actor Read Model<br/>脱敏状态与当前精确 Offer"]
        OPERATION["Operation 队列与状态机<br/>queued → running → terminal"]
        OPSTORE["持久 Operation Store<br/>进程锁、保留与对账"]
    end

    GAME --> CAPTURE
    GAME --> OFFER
    CAPTURE --> SDK
    OFFER --> SDK
    SDK -->|"提交 Observation、Decision Window 与 Offer"| API
    EMBED --> ENGINE
    API --> ENGINE
    ENGINE <--> MEMORY
    ENGINE --> STORE
    ENGINE --> JOB
    JOB --> POLICY
    POLICY --> LOCAL
    POLICY --> PROVIDER
    PROVIDER -->|"最小有界 Prompt Packet"| MODEL
    MODEL -->|"结构化 DecisionDraft"| PROVIDER
    LOCAL --> VALIDATE
    PROVIDER --> VALIDATE
    VALIDATE -->|"合法"| PROPOSAL
    VALIDATE -->|"无效、越界或过期"| LOCAL
    PROPOSAL --> API
    API -->|"返回提案或异步 Job 结果"| SDK

    AGENT -->|"MCP Tool"| MCP
    MCP -->|"本机 HTTP"| DAEMON
    DAEMON --> AUTH
    AUTH --> READMODEL
    AUTH --> OPERATION
    OPERATION --> OPSTORE
    SDK -->|"register / renew / publish"| DAEMON
    SDK -->|"poll 领取 Operation"| OPERATION
    OPERATION -->|"消息、Directive 或精确 Offer"| SDK

    SDK -->|"内部提案或外部 Operation"| EXECUTOR
    EXECUTOR -->|"复核 Owner、Epoch、Deadline、Digest、权限和当前状态"| WORLD
    WORLD -->|"Applied / Rejected / Running / Outcome"| EXECUTOR
    EXECUTOR --> SDK
    SDK -->|"ReportAction / Outcome Outbox"| API
    API --> ENGINE
    SDK -->|"ACK / Run / 权威 Outcome"| DAEMON
    DAEMON -->|"get_operation / wait_operation"| MCP
    MCP -->|"可报告结果"| AGENT
```

## 读图顺序

1. 游戏 Host 捕获可信观察，并生成已经绑定参数、目标、Epoch 和截止时间的 Offer。
2. 内部决策走 Runtime：Rin 从本地策略或在线模型取得草案，再由本地验证器生成提案。
3. 外部决策走 Host Control：Agent 通过 `rin-mcp` 读取状态或选择 Host 已发布的完整 Offer。
4. 两条路径最终都回到游戏 Host 的同一个执行器；Rin 和 MCP 都不能直接修改游戏世界。
5. Host 在权威线程重新检查世界状态并执行，然后分别向 Runtime 或 Control Plane 回报结果。
6. 只有带 Host 权威 Outcome 的终态 Operation 才能证明动作已经执行成功。

## 关键边界

- **世界权威在游戏侧**：Rin 不实现引擎对象、逐帧移动、导航、物理、战斗或任意脚本执行。
- **模型只做有界选择**：模型不能增加坐标、参数、Capability 或未被 Host 提供的动作。
- **两个服务彼此独立**：`rin` Runtime 管理角色状态与决策；`rin-control` 管理外部控制与投递。
- **MCP 代理无状态**：多个 Client 可以各自运行 `rin-mcp`，退出代理不会关闭 daemon。
- **持久化责任分层**：Rin 保存角色事件与快照；Host 保存 Pending Turn、效果标记和 Outcome Outbox。
- **失败时关闭权限**：过期 Epoch、失效 Offer、控制权修订变化、未知字段和权限不足都会被拒绝。

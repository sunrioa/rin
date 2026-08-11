# Host 集成验收

[English](host-integration-validation.md) | [简体中文](host-integration-validation.zh-CN.md)

契约、单元测试和 Headless GameTest 是发布门禁，但不能证明 NPC 在真实游戏里自然、
稳定且不会破坏玩家资产。每个 Adapter 都要分别记录自动证据与真人证据。

## Rin 核心门禁

```bash
make verify
make build
```

`make verify` 覆盖：

- Go Format、Vet、Race 和全包测试；
- Host、Control 与 Agent OpenAPI 契约一致性；
- Python、JavaScript、C#、Java、Lua SDK；
- Grid、Story、Terminal 三个 V2 Adapter 流程；
- MCP Tool、权限和官方协议相关测试。

构建成功只证明 Rin 核心；具体游戏仍需自己的 Loader、服务端、存档和 UI 测试。

## Adapter 自动化最低要求

### 契约

- Manifest、Capability Schema 和 Digest 可重复；
- Observation 有界、分页、严格 JSON 且 Epoch/Sequence 正确；
- HostRef 不可伪造、过期引用失败关闭；
- Binding 不修改世界，Effect 完全由 Host 生成；
- Output 符合 Capability Output Schema。

### 权威线程

- 所有游戏读写在 Server/Main/Authority Thread；
- HTTP、模型和磁盘等待不阻塞 Render/Server Tick；
- 迟到回调无法在新 Epoch 修改旧世界；
- 同一个 Operation 精确重投不会重复世界效果。

### Policy 与权限

- 未知 Effect、Scope、Ownership 默认拒绝；
- 每个 Profile、Rule、Budget 和确认路径有测试；
- Controller Lease 过期、Authority Revision 变化和 Emergency Stop 都会阻止动作；
- 多人默认关闭自主控制，显式开放后仍受区域、资产和预算约束；
- critical 能力不能绕过 Owner/Admin 确认和游戏原生权限。

### Operation

- `queued` 不被报告为成功；
- Host 从未领取时得到 `stale` 和 `delivery_attempts=0`；
- ACK、Run 和 Outcome 顺序、重复和倒退都被校验；
- `outcome-unknown` 可以由迟到 Host Outcome 对账；
- Cancel 与 Emergency Stop 不声称回滚；
- Macro 的每个世界修改都是可审计 Child Operation。

### 安全

- Token、API Key、文件路径和私有 Prompt 不出现在协议、日志、存档或夹具；
- 配置和状态拒绝符号链接、路径穿越、重复 JSON 字段和超限输入；
- 未知第三方物品、方块、实体或组件不会仅凭名称被视为安全；
- 自动拾取、容器、战斗和破坏行为遵循可配置资产策略。

## 故障注入矩阵

在真实存档副本中，分别于以下时点正常退出和强制终止：

1. Action 尚未提交；
2. Control 已持久入队但 Host 尚未 Poll；
3. Host 已收到但尚未 ACK；
4. Host 已 ACK、尚未开始实时控制器；
5. 世界已改变、Outcome Outbox 尚未写入；
6. Outcome 已写入、Daemon 尚未确认；
7. Controller Lease、Host Lease 或确认即将过期；
8. 长任务运行中切换世界、读档或卸载 Actor。

每次恢复后检查：

- Operation ID、Idempotency Key 和 Applied Marker 保持一致；
- 已执行效果不会重复；
- 未执行的旧 Epoch 动作不会复活；
- Outcome Outbox 最终排空，无法证明的结果明确为 `outcome-unknown`；
- Host 重新注册和发布 Read Model 后才能接收新任务；
- 内部 Task 和外部 MCP 都看到相同终态。

## 真人单人验收

至少完成一次新存档和一次已有存档的长时间游玩：

- 安装、启动、配置、禁用和更新流程可理解；
- 内部模式能对话、观察、主动提出并直接执行允许的安全行动；
- 外部 MCP 模式不调用内部思考器，仍可完成相同 Capability；
- 角色能组合原子能力完成一项连续工作，并在失败后合理重新规划；
- 急停在可接受时间内停止导航、采集、战斗和建造；
- 世界切换、死亡、睡眠、容器关闭和目标消失不会卡死；
- UI、日志和错误能区分“已排队、执行中、已完成、失败、结果未知”；
- 游戏帧率或服务端 Tick 没有不可接受的停顿。

## 真人多人验收

在局域网和代表性专用服务器分别验证：

- 自主模式默认关闭，只有有权玩家可以开启；
- Actor 所有者、控制者和其他玩家身份不会混淆；
- 不攻击驯服、命名、拴住或受保护的资产；
- 不读取或移动未授权容器和玩家物品；
- 区域保护、服务端权限、命令白名单和急停优先于模型意图；
- 两个外部 Agent 竞争同一 Actor 时只有一个 Lease 成功；
- 多个 Actor 和多个 MCP Client 不会互相串 Operation 或 Outcome。

## 行为自然度验收

自动测试不能判断“像活人”。真人应记录：

- 是否会结合当前活动、近期失败和长期偏好调整表达；
- 是否会主动提出合理小目标，而不是刷屏或无限循环；
- 是否知道何时等待、询问、拒绝或停止；
- 是否会重复同一行动、同一句话或同一片区域；
- 记忆引用是否有证据、是否把推测误当事实；
- Token、延迟和模型费用是否匹配实际玩家价值。

该部分只影响 Persona、Memory、Skill、模型提示和 Adapter 观察质量，不得通过降低
Policy 或资产保护来“提高自主性”。

## 发布证据

每次验收记录：

- Rin 与 Adapter Commit、构建 Artifact SHA-256；
- 游戏、引擎、Loader、OS、Java/.NET/Runtime 和 Mod 列表；
- 单人/多人类型、权限 Profile、模型和 Provider；
- 测试步骤、预期、实际、脱敏日志和最终 Operation；
- Tick/帧时间、模型延迟、Token、任务成功率和人工干预次数；
- 已知问题与明确未验证项。

只有真实故障测试证明同一 Operation 不重复效果，才能声明
`idempotent-action`；只有游戏效果和 Outcome 由同一真实事务提交，才能声明
`transactional-action`。

## 停止条件

自动工作完成后，只应剩下三类人工项：真实游戏完整试玩、GUI/安装体验确认、角色
自然度与语音/交互主观评价。任何可通过代码、Headless Server、Fixture 或静态扫描
复现的问题都不应推给真人验收。

# Security

[简体中文](SECURITY.md) | [English](SECURITY.en.md)

本文定义 Rin Harness V2 的安全边界。当前源码为 `0.7.0` Preview；Preview 不会
放宽失败关闭、秘密隔离和游戏权威原则。

## 信任模型

Rin 将以下组件视为可信计算基：

- 权威游戏 Host 与 Adapter；
- 游戏所有者或服务器管理员提供的身份、Scope 和策略配置；
- 本机 `rin-control` 进程及其私有数据目录。

以下内容始终不可信：

- 模型响应、外部 Agent 和 MCP 参数；
- 玩家对白、模组文本、记忆正文、人格正文和 Skill 正文中的指令；
- Adapter 未验证的任意 JSON、远端 Provider 响应和网络错误正文。

Host 负责说明真实世界状态。模型或外部 Agent 只能提交 `ActionRequest`，不能
声明 Effect、所有权、风险、可逆性、策略结果或执行成功。

## 强制执行链

每个世界修改必须经过：

1. 有效且独占的 Controller Lease；
2. 与当前 Actor、Observation Sequence 和 Epoch 绑定的 `ActionRequest`；
3. Host 在权威线程完成的目标解析和 `BoundAction`；
4. Host 生成的 Effect Preview；
5. 确定性 Gameplay Policy 的允许、拒绝或单次确认；
6. 可持久恢复的 Operation 投递；
7. Host Run、Outcome 和 Evidence。

内置策略安全内核不可由配置关闭，并拒绝以下 Effect Kind 或 Tag：任意代码、文件
访问、原生调用、权限伪造和秘密泄露。未知 Effect、未知 Scope 和未知所有权同样
失败关闭。`open` 或 `privileged-custom` Profile 也不能绕过安全内核。

Capability Discovery 不是授权。Macro 必须通过可审计的子 Operation 执行世界
修改，不能获得旁路。游戏还必须在执行前重新验证对象、距离、区域、所有权、
资源、冷却和当前世界状态，防止 TOCTOU。

## 执行结果

以下状态均不能作为游戏已执行的证明：`queued`、`awaiting-confirmation`、
`delivered`、`accepted`、`running`。只有 Host 提交终态 Outcome 后，Control Plane
才会返回 `execution_confirmed=true`。

客户端超时、取消等待、连接中断或 `outcome-unknown` 不证明行动没有发生。调用方
必须用同一个 Operation ID 对账，不能创建语义相同但身份不同的重试。

Host Lease、Controller Lease、Authority Revision、Epoch 和 Observation Sequence
用于拒绝断线后、换控制器后或旧时间线中的命令。Emergency Stop 独立于 Controller，
对内部和外部来源同样生效。

## 本机网络边界

`rin-control` 只允许监听回环地址，默认是 `127.0.0.1:7375`。Control API 要求：

- `RIN_CONTROL_TOKEN` 至少 32 字节；
- Principal 与 Scope 由 Daemon 启动配置固定，而不是从请求正文读取；
- HTTP 请求使用 Bearer 鉴权；
- SDK 禁止 Redirect，并限制超时、响应体、JSON 深度和安全整数。

当前版本不提供把 Control API 直接暴露到公网的受支持模式。需要跨主机控制时，
应由部署方提供经过独立审计的认证代理和网络隔离；这不属于当前支持边界。

`rin-mcp` 是 STDIO 薄代理，只访问同机 Control Daemon。它不拥有游戏状态、不启动
第二套执行器，也不把 MCP 的工具调用解释为成功。一个 MCP 安装可以访问当前
Principal 可见的多个兼容 Host。

## 内部模型与 Prompt Injection

Internal Agent 把机器选择的允许 Capability、Target Handle、Epoch 和预算放在
可信 Contract 中，把 Persona、Memory、Skill、Observation、玩家文本和能力描述
放在 `untrusted_context`。模型输出必须匹配封闭 JSON Schema，并再次校验：

- 只能引用 Contract 列出的 Capability、版本和目标；
- 最多进行一次有界 Capability/Skill 详细检查；
- 记忆候选是带置信度和 TTL 的主观假设，不是权威世界事实；
- 模型不能报告行动成功，不能生成直接执行的代码或引擎调用。

即使模型通过 Prompt Injection 产生越权意图，Host Binding、Policy 和 Adapter
执行前校验仍必须拒绝它。

## Provider 与秘密

内部 Agent 配置文件不得包含 API Key。凭据只通过以下环境变量进入进程：

- `RIN_CONTROL_TOKEN`：Control API；
- `RIN_AGENT_TOKEN`：Agent Task API，必须与 Control Token 不同；
- `RIN_AGENT_API_KEY`：可选模型 Provider Key，不能与任一 Daemon Token 相同。

远端模型 URL 必须使用 HTTPS；仅回环 Provider 可以使用 HTTP。URL 不接受 userinfo，
Provider Client 禁止 Redirect，并限制响应大小、超时、重试和熔断。错误不会回显
API Key 或完整 Provider Body。

不得把 Token、API Key、私有 Agent 配置、文件路径或未筛选的玩家数据写入：

- Observation、Capability、Action、Outcome 或审计摘要；
- Persona、Memory、Skill 或游戏存档；
- MCP 工具输出、日志、测试夹具或版本库。

如果凭据曾出现在聊天、截图、终端历史或提交中，应立即在供应商侧撤销并轮换；
从 Git 历史删除文本不能使已经暴露的凭据重新安全。

## 本地状态

Control Operation、Agent Task、Memory 和相关状态目录使用单写者进程锁。第二个进程
不能同时打开同一目录。更新使用有界 JSON、临时文件、同步和原子替换；调用方仍应
把整个数据目录视为敏感内容，并使用受限账户和本地文件系统。

不支持把状态目录放在 NFS、SMB、云同步目录或多个进程共享的路径上。复制或备份
状态前应停止 Daemon，或使用能够提供一致性快照的外部机制。

## Adapter 责任

Rin 无法替游戏判断所有具体规则。Adapter 必须：

- 只在服务器或游戏权威线程修改世界；
- 不把对象指针、任意路径、命令执行器或私有 API 暴露给 Controller；
- 对多人、公开服务器、命令、容器、玩家资产和破坏行为提供明确的本地权限；
- 将导航、战斗和其他实时动作保持可中止，并定期重新检查环境风险；
- 使用可信结果构造 Outcome，不从模型文案推断执行结果。

所谓“最高权限”只应开放 Adapter 已注册、Host 能绑定、Policy 能检查的能力，
不等于开放 Shell、任意代码或绕过游戏服务端权限。

## 漏洞报告

请通过 GitHub 仓库的私密安全报告渠道提交漏洞。不要在公开 Issue 中附带 Token、
API Key、私有 Agent 配置、玩家存档、完整 Operation 数据或可复现的秘密内容。

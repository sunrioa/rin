# Security

## Defaults

- 服务默认只监听 `127.0.0.1`。
- 非 loopback 地址必须同时传入 `-allow-remote` 并设置 `RIN_TOKEN`。
- Rin v0.2 不提供入站 TLS；远程部署必须放在受控网络和 TLS 反向代理之后。
- 除 `/health` 外，配置 Token 后所有端点都使用 constant-time Bearer 校验。
- JSON 正文默认限制为 32 MiB（主要用于完整快照），未知字段、多个 JSON 值和非 UTF-8 内容被拒绝。
- Session ID 只能使用安全标识符，HTTP 请求不能提供文件路径。
- 事件和快照权限为 `0600`；快照以不可变文件原子写入。
- API Key、Sidecar Token 和供应商配置不属于协议状态，不会持久化。
- 供应商 URL 禁止 userinfo、query、fragment 和自动 HTTP 重定向；远程模型默认强制 HTTPS。

## Trust model

Policy 和模型输出均不可信。运行时只接受游戏本次声明的候选动作，并核对 Actor、Goal、Memory、Boundary、revision 和 content binding。Rin 不执行脚本、Shell、动态插件或模型生成的工具调用。

在线模式只发送当前 Actor 的有限 traits、boundaries、active goals、相关 memories、beliefs、近期行动及本次候选动作。事件日志、完整 Session、Receipts、快照、文件路径、Token 和 API Key 不进入模型数据包。所有游戏文字放在明确标记的 `untrusted_game_data` 下，模型返回值仍需本地白名单验证。

游戏仍需把任务、物品、战斗、金钱、亲密同意和关键剧情等高权限操作留在自己的规则层验证。

同一数据目录只允许一个 Rin 进程写入。需要高可用或多实例时，应由宿主提供单写者协调，或实现新的 Store。

## Reporting

请通过 GitHub 仓库的私密安全报告渠道提交漏洞，不要在公开 Issue 中附带 Token、API Key、存档或完整事件日志。

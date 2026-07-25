# Security

[简体中文](SECURITY.md) | [English](SECURITY.en.md)

本文说明 Rin 的受支持安全边界、部署要求和漏洞报告方式。

Rin `0.6.0` 是 Preview、pre-1.0 软件。Preview 状态不会放宽本文的 Fail-closed
规则，但未来兼容性必须结合 Changelog 与迁移指南重新评估。

## Defaults

- 服务默认只监听 `127.0.0.1`。
- 非 loopback 地址必须同时传入 `-allow-remote` 并设置 `RIN_TOKEN`。
- Rin 不终止入站 TLS；远程部署必须放在受控网络和 TLS 反向代理之后。
- 除不含内容的 `/health` 与 `/ready` Probe 外，配置 Token 后所有端点都使用
  constant-time Bearer 校验。`/metrics` 与 `/v1/diagnostics` 仍需鉴权，不得作为
  Reverse Proxy 的公网 Route。
- JSON 请求正文与随附客户端响应正文默认限制为 32 MiB。完整 inline Snapshot
  compact JSON 另有 16 MiB 上限，以便为 envelope 与持久记录预留空间；超限
  时返回 `413 snapshot_too_large`，绝不会截断。大 lineage 使用经过相同
  Bearer 校验的 NDJSON Session Transfer；导入 Binding 由独立可信 header
  提供，压缩正文会被拒绝，只有校验 complete 后才原子发布。未知字段与多个
  JSON 值会被拒绝。HTTP API 会在 JSON 解码前校验原始
  请求正文；非法 UTF-8 字节或未配对 JSON Unicode Surrogate 返回
  `400 invalid_json`。
- 每个公共 HTTP JSON 整数都必须能在 `-9007199254740991` 至
  `9007199254740991` 之间精确表示；字段自己的非负和更窄约束仍然有效。跨语言
  边界不会舍入不安全整数，而是直接拒绝。
- Session ID 只能使用安全标识符，HTTP 请求不能提供文件路径。
- 在 Unix 上，事件、索引、checkpoint、Snapshot 与锁文件权限为 `0600`，数据
  目录为 `0700`。Windows 不解释 POSIX mode bit，这些文件继承数据根目录 ACL；
  部署方必须把该 ACL 限制到 Sidecar 账户。Snapshot、checkpoint 与重建索引使用
  已同步的临时文件、rename 和 directory sync 发布。
- 事件日志采用 `retain_forever`；File Store 默认保留每个 Session 最近 2 个
  有效 checkpoint 与最近 2 个有效 Snapshot 文件。备份与删除策略必须把所有
  保留 artifact 都视为敏感数据。
- Session 删除必须先 Archive，并携带可信 Binding、精确 Head、Archive Receipt
  与完整 Session-ID Confirmation。删除会保留不含玩家内容的最小 Tombstone，
  以支持幂等重试并永久阻止 ID 复用；外部 Export、游戏存档、备份和供应商副本
  不在 Rin 删除范围内。
- API Key、Sidecar Token 和供应商配置不属于协议状态，不会持久化。
- Request Log 只包含有界 Correlation ID、HTTP Method、匹配后的 Route
  Template、Status 与 Duration。运维 Diagnostics 只使用聚合计数和有界
  Status/Error 分类，不包含玩家内容或每 Session Label。
- 供应商 URL 禁止 userinfo、query、fragment 和自动 HTTP 重定向；远程模型默认强制 HTTPS。
- 官方游戏适配器同样禁止重定向；明文 Sidecar HTTP 只允许显式 loopback，远程 HTTPS 必须配置 Token。

## Trust model

Policy 和模型输出均不可信。运行时只接受游戏本次声明的候选动作，并核对 Actor、Goal、Memory、Boundary、revision 和 content binding。Rin 不执行脚本、Shell、动态插件或模型生成的工具调用。

Snapshot 是可信、不透明的序列化状态，必须按事件日志同等级别保护。其中
SHA-256 canonical checksum 可发现意外损坏或未同步修改，但不是签名或来源
证明，修改者可以重算。Restore 因此要求 `expected_binding` 来自运行中游戏
的可信内容 manifest，而不是信任导入 Snapshot 自己声明当前启用的内容。

Event Chain Hash 具有相同的对抗性限制：它是无密钥 SHA-256 一致性链接，不是
签名或 MAC。能替换完整 Event Log 的一方可以重算事件链及其 Index、Checkpoint
与 Snapshot。Rin 能发现不一致的 History，但不能在缺少外部不可变锚点时认证
History。

在线模式只发送当前 Actor 的有限 traits、boundaries、active goals、相关 memories、beliefs、近期行动及本次候选动作。事件日志、完整 Session、Receipts、快照、文件路径、Token 和 API Key 不进入模型数据包。所有游戏文字放在明确标记的 `untrusted_game_data` 下，模型返回值仍需本地白名单验证。

模型输出 Schema 不接受 `summary` 或 `rationale`，所有 Policy Draft 中的兼容
文本也会被丢弃。运行时只用游戏明确授权展示的 `ActionSpec.description` 与
固定 stance 模板重建玩家字段；私有 Goal、Boundary、Memory、Belief、Prompt
和供应商文本不是该函数的输入。`policy_source`、`recalled_memory_ids`、
`goal_id`、`boundary_id` 与完整 `proposed_goal` 是私有审计/集成元数据，不得
直接展示给玩家。Action 中只有游戏明确授权的 Description 可作为玩家文案；
ID、Kind、Target 和 Parameter 同样默认属于集成数据。该边界依赖输入隔离与
按构造重建，不依赖私密字符串黑名单；游戏必须确保候选动作描述本身可公开展示。

升级后，`rin.reducer-projection/v2` 会在 State、Replay、Snapshot 导出与
exact retry 等 API 投影中重建旧 Proposal 的展示文本，但不会改写权威事件链。
旧 `proposal.created` 记录或 Restore 事件内嵌的旧 Snapshot 仍可能在磁盘、
备份及外部 Store 中保留原 Summary/Rationale；升级不是隐私擦除，仍须按敏感
事件日志处理这些原始数据。

结构化 Generation 会把调用方提供的 messages 发送给模型，但不会自动附加 Session、事件日志、路径或凭据。Rin 只验证顶层 JSON Object、字符与字节上限；调用方必须再验证自己的字段 Schema、引用 ID、许可与 Canon，不能直接执行输出内容。

内置 Provider 不会把 Prompt、凭据或原始 Provider HTTP 正文放入错误、日志或
持久 Session State。成功解码并验证的 Generation Result 会有意保留在有界进程内
Job Record 与 Semantic Cache 中，再返回调用方。这些 Content 不可信，也可能敏感；
游戏若持久化，必须应用自己的 Allowlist 和 Retention Policy。Provider JSON
成功响应会在解码前严格拒绝非法 UTF-8 与未配对 Unicode Surrogate。非 2xx
Provider Body 只用于有界错误分类，不会被当成 Content、写入 Session State，
也不提供 Content 有效性保证。

游戏仍需把任务、物品、战斗、金钱、亲密同意和关键剧情等高权限操作留在自己的规则层验证。

适配器的 `offline.*` Proposal 只用于游戏自己的离线回退，明确标记 `committable=false`，不能伪装成 Sidecar 提案提交。线程、HTTP 对象和取消句柄不得进入 Ren'Py 存档；只有普通 JSON 结果和经验证 Snapshot 可持久化。

已确认存活的 Sidecar Proposal Operation 内部若 Provider 失败，可以使用确定性
Policy；Sidecar Submit、Poll、Timeout 或 Cancel 结果未决则不是 Offline 证明，
在线 Proposal 可能已经存在。游戏必须持久化并恢复完全相同的 Proposal
Attempt/Job 身份，阻塞新 Turn，并在确认 Proposal 不存在前禁止执行 Fallback。

随附 File Store 会在读写前取得数据目录的 non-blocking exclusive lock。第二个
进程打开同一目录会失败；嵌入式调用方必须调用 `(*store.File).Close()` 释放
lease。`darwin` 与 `linux` 使用 `flock`，`windows` 使用无共享模式的独占文件
handle。其他所有 GOOS 上，`store.OpenFile` 返回
`ErrDataDirectoryLockUnsupported` 并 fail closed。高可用或多实例宿主必须实现
另一个外部协调 Store，不能共享 JSONL 目录。

随附 JSONL Store 只支持本机独占文件锁、同目录原子 rename 与下述平台持久化
primitive 语义可靠的本地文件系统。不支持 NFS、SMB、FUSE mount 和云同步目录；
远程或共享存储必须使用外部协调的 Store。

Unix 使用 file/directory `fsync`。Windows 使用 `FlushFileBuffers` 同步文件，
并以 `MoveFileExW(MOVEFILE_WRITE_THROUGH)` 发布 rename，因为 Windows 没有
规定可对 directory handle 调用 `FlushFileBuffers`。这些操作会缩小崩溃窗口，
陈旧派生索引会从权威事件日志重建，但不是针对存储硬件、kernel、filesystem、
备份或运维故障的绝对持久性保证。复制数据目录前应停止 Sidecar，或使用协调
一致的存储快照。

## Reporting

请通过 GitHub 仓库的私密安全报告渠道提交漏洞，不要在公开 Issue 中附带 Token、API Key、存档或完整事件日志。

# 可扩展 Session Transfer 设计

[English](session-transfer.md) | [简体中文](session-transfer.zh-CN.md)

## 状态

Session Transfer 已实现端到端支持：protocol frame、Store staging、Runtime
streaming、Bearer 保护的 HTTP export/import、JavaScript/C# stream helper，
以及超过 16 MiB 的 fail-closed 验收。现有 inline Snapshot/Restore 保持兼容，
并继续执行 16 MiB 上限。

## 问题

Rin 的权威 Event Log 与 Identifier History 按 lineage 永久增长，而 portable
Snapshot 通过单个 JSON Object 传输，并具有 16 MiB compact JSON 上限。长寿命
Session 最终会无法通过现有 HTTP API Snapshot、Replay 或 Restore。简单提高正文
上限只会推迟问题，并会重新引入无界内存、代理限制和失败后全量重传。

Session Transfer 必须在运维方配置的资源边界内，为 lineage 提供有界内存、
可验证、可恢复的导出与导入，同时保持游戏世界权威、Event Record 原始字节与
hash chain、完整 Identifier History、Restore Binding 信任边界、永久 ID 身份
以及 inline Snapshot 兼容性。

## 决策

### 1. 使用 framing 流

Transfer 使用 UTF-8 NDJSON framing，每一行是带明确 `type` 的封闭 JSON Object。
顺序固定为一个 `manifest`、一个或多个 `event`，最后一个 `complete`。

Manifest 包含 transfer/protocol/projection 版本、Session ID、Binding、起止
revision/head、event count、随机 `transfer_id` 和算法上限。每个 event frame
包含原始 `EventRecord` 及其 canonical JSON bytes 的 SHA-256；导出端不得重写
Data、RecordedAt、Hash 或其他权威字段。

Complete frame 重复终止 revision、head 和 event count，并携带按 frame 顺序计算
的 stream SHA-256。导入只有完整读取并验证 complete 后才可发布 Session。

#### Version 1 frame 与 hash profile

`rin.session-transfer/v1` 只支持完整 lineage：`start_revision` 必须为零、
`start_head_hash` 必须为空、`event_count` 必须大于零，并且
`terminal_revision` 必须等于 `event_count`。算法只允许小写十六进制 SHA-256
（`hash_algorithm: "sha256"`）。Revision、count 和 lineage generation 必须是
不超过 `9007199254740991` 的精确 JSON integer。
Lineage generation 是 durable Restore event 的 zero-based count，因此普通
Create 创建的 Session generation 为零。

Checksum 输入是 protocol struct 所声明 wire member 顺序生成的 compact UTF-8
JSON，不含无意义空白。`EventRecord.Data` 保持原 compact JSON 的 member 顺序和
value 表示。单 event checksum 是 compact `EventRecord` Object 的 SHA-256。
Stream checksum 按顺序覆盖 compact manifest 加 LF，以及每个 compact event
frame 加 LF；不包含 `complete` frame。跨语言实现必须通过
`protocol/transfer_test.go` 中的 golden vector；把 JSON 解析为无序 Object 后用
语言默认顺序重新序列化不符合契约。

校验器会拒绝非 genesis 起点、不安全整数、非法时间或 JSON、checksum 不匹配、
sequence gap、断裂的 `prev_hash`、多余 event，以及与 manifest 或最终 event
不一致的终止边界。Runtime replay 还会单独验证权威 `EventRecord.Hash` chain；
传输 checksum 不能替代它。

### 2. 每个 frame 保持有界

- HTTP 逐 frame 读写，不 materialize 完整 Transfer。
- EventRecord 继续服从 Store 的单记录读取上限。
- manifest、complete 和 envelope 使用更小的独立上限。
- SDK 写入调用方提供的 stream/file sink，不返回巨大字符串。
- 导入接受 stream/file source，不要求完整 byte array。

正文总长度不受 16 MiB inline Snapshot 上限限制，但 Runtime 默认仍限制单次
Transfer wire bytes 为 1 GiB、事件数为 1,000,000、全局并发为 4，并限制同一
Session 同时只能有一个 Transfer。可通过 `-transfer-max-bytes`、
`-transfer-max-events`、`-transfer-max-concurrent` 或对应的
`RIN_TRANSFER_MAX_BYTES`、`RIN_TRANSFER_MAX_EVENTS`、
`RIN_TRANSFER_MAX_CONCURRENT` 环境变量配置。`transfer_too_large` 与
`transfer_event_limit` 映射为 HTTP 413，全局 `transfer_capacity` 映射为
429，同 Session `transfer_in_progress` 映射为 409。字节预算包含 manifest、
Event、LF 与 complete framing；Import 会在写入违规 Event 前拒绝，Export 会在
发送违规 frame 前拒绝。达到限制时不能发布部分 Session。

### 3. 导出固定 immutable boundary

导出开始时在 Session lock 下只捕获目标 revision/head 与 lineage generation，
随后释放 mutation lock。导出读取不晚于该 boundary 的 Event range，之后发生的
mutation 不进入本次 Transfer。

导出必须验证 range 连续、起点、每个 previous hash、终点及捕获的 lineage
generation。首版只导出完整本地 Event Log。未来的增量 Transfer 必须显式携带
调用方可信持有的 base revision/head，不能只凭 revision 拼接。

### 4. 导入必须原子发布

导入不能直接逐条写入 live Session。Store 增加可选 `TransferStore` 能力：

1. 在同一数据根目录创建不可见 staging area；
2. 顺序验证并写入 frames；
3. 同步 staging 文件与目录；
4. 验证完整 chain、Binding、终止 anchor 与 stream checksum；
5. 通过同目录 atomic rename 一次发布；
6. 同步父目录。

解析、校验、取消、容量与 Rename 之前的失败只能清理或保留不可见 Staging。
Atomic Rename 之后若父目录 Sync 失败，完整 Target 可能已经可见但其持久结果仍
不确定；它绝不会是部分 Session。`TransferRecoveryStore` 会重新 Fence 并校验
完全相同的 Manifest Boundary；客户端重发同一 Import Stream 后即可确认并注册。
不同 Manifest 或 Stream 仍返回 `session_exists`。

未实现 `TransferStore` 的自定义 Store 继续支持现有 API，但 Import 返回
`transfer_unavailable`；Runtime 不用 `Create`/`Append` 模拟非原子导入。

### 5. 导入目标与 Binding

首版只允许导入为尚不存在的同名 Session，避免隐式覆盖和 lineage 合并。请求必须
显式携带来自运行中游戏可信 manifest 的 `expected_binding`；它必须与 Transfer
manifest 及首事件恢复出的 Binding 一致。

覆盖、改名、分支合并和导入到已有 Session 不属于首版能力。回档继续使用 Restore；
迁移超大 Session 使用完整 Transfer。

### 6. Identifier History 不截断

完整 Event Log，包括 Restore event 中携带的历史，必须能确定性重建 Identifier
History。原子导入后 Runtime 从 genesis 验证并重放一次，再注册 live Session。
checkpoint 可随后异步创建，但不能代替第一次完整验证。

Transfer 不允许只导出有界 State 或删除 tombstone，否则放弃分支中的 ID 会重新
可用，破坏 exact retry 和 outcome reconciliation。

### 7. HTTP 与取消语义

配置 `RIN_TOKEN` 后，现有两个由 Bearer 保护的 Operation：

- `POST /v2/session/export`：小型 JSON 请求，NDJSON streaming response；
- `POST /v2/session/import`：请求正文为 NDJSON stream；可信 Binding 独立通过
  必填的 `Rin-Expected-Game-Id`、`Rin-Expected-Content-Id`、
  `Rin-Expected-Content-Version` 与 `Rin-Expected-Content-Hash` header 传入。

未配置 Token 的开发 Sidecar 仍执行[部署与监控](operations.zh-CN.md)中的
Loopback Host 与 Browser 同源规则；远程 Transfer 始终要求 Token。

Export 接受 `application/json` 并返回 `application/x-ndjson`；Import 要求
`application/x-ndjson`。Import 只允许未设置 `Content-Encoding` 或设置为
`identity`，其余全部拒绝。Manifest、complete 与 error frame 上限为 32 KiB；
event frame 上限为 Store 的 64 MiB EventRecord 限制加 32 KiB framing。每个
frame 必须以 LF 结束、是有效 UTF-8 JSON、符合预期 closed object shape，并严格
按声明顺序出现。服务端按 backpressure 消费数据，并在 frame 之间检查请求取消。

普通 API 与 Transfer stream 使用独立服务端预算：
`-request-timeout` / `RIN_REQUEST_TIMEOUT` 默认 30 秒，
`-transfer-timeout` / `RIN_TRANSFER_TIMEOUT` 默认 30 分钟。生产
`http.Server` 不再用原来的 15 秒 ReadTimeout / 30 秒 WriteTimeout 截断
Transfer，而由 route-aware deadline 控制。独立的 rolling 30 秒读写 inactivity
deadline 可通过 `-transfer-inactivity-timeout` /
`RIN_TRANSFER_INACTIVITY_TIMEOUT` 配置；持续传输会刷新 deadline，停滞连接不会
一直占用并发容量。JavaScript 与 C# 客户端的 Transfer 操作独立默认 2 分钟；
较大部署分别配置 `transferTimeoutMs` 或 `RinClientOptions.TransferTimeout`。
调用方 cancellation 始终优先。

Export response 一旦开始，后续失败只写一个包含普通有界 `ErrorDetail` 的终止
`error` frame，绝不能再写 `complete`。Import 只有验证 `complete` 且随后读到
EOF 才算成功。截断、多余 frame、损坏、取消或 Binding 失败都会 abort 不可见
staging，不发布 Session；publish 前的请求取消返回
`408 transfer_cancelled`。HTTP 与测试均不记录 Event Data。

### 8. inline Snapshot 保持兼容

现有 Snapshot/Restore 继续作为普通 Session 的游戏存档路径，保持 JSON Shape 与
16 MiB 上限。Session Transfer 是大 lineage 的迁移、备份与恢复通道，不隐式改变
Snapshot endpoint 的媒体类型。

## 拒绝的方案

- **只提高 Snapshot 上限：**仍要求服务端、SDK 和游戏分配完整对象，没有断点或
  frame 校验。
- **只导出当前 State：**会丢失审计、Replay 和永久 Identifier History。
- **通过 Create/Append 边接收边发布：**中断会暴露部分 Session。
- **只在 HTTP Manager 内存 staging：**重新引入无界内存，重启后也无法安全诊断。

## 实施顺序

1. 定义 protocol frame、校验器和 hash 规则；**已实现。**
2. 定义 `TransferStore`，实现 File Store staging/atomic publish；**已实现。**
3. 实现 Runtime immutable export boundary 和 import 后 genesis verify；**已实现。**
4. 增加 HTTP stream；**已实现。**
5. 增加 TypeScript/C# SDK stream helper；**已实现。**
6. 增加超过 16 MiB 的端到端、取消、损坏和崩溃恢复测试；**已实现。**
7. 更新兼容、安全、迁移、发布和运维文档；**已实现。**

Roadmap 只在第 6 步通过后把 Session Transfer 标记为已支持。

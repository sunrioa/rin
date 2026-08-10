# Rin C# SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 .NET 6+ 的异步客户端，并提供适用于 Unity Mono 等宿主的
.NET Standard 2.0 兼容构建。

`Rin.Client` 使用 `HttpClient` 和 `System.Text.Json`。.NET 6 构建无需额外
Package Runtime；.NET Standard 2.0 构建会固定 `System.Text.Json` 及其运行时
依赖。在插件或游戏生命周期内复用一个 Client。

```csharp
using Rin.Client;

using var rin = new RinClient(new RinClientOptions
{
    BaseUrl = "http://127.0.0.1:7374",
    Token = Environment.GetEnvironmentVariable("RIN_TOKEN") ?? "",
});

var capabilities = await rin.NegotiateCapabilitiesAsync();
Console.WriteLine(capabilities.ReleaseVersion);
```

外部控制器使用与 MCP 共用的 Control V2 客户端：

```csharp
using var control = new RinControlClient(new RinControlClientOptions
{
    Token = "a-local-secret-containing-at-least-32-bytes",
});
var worlds = await control.ListWorldsAsync();
```

它覆盖角色观察、能力发现、控制租约、行动提交与确认、Operation 等待/取消和
急停；只允许本机回环连接并强制使用 Token。只有带 Host Outcome 的 Operation
终态才能证明游戏已执行。

能力协商会在 Runtime 不是 `rin.protocol/v2` 或不支持权威 Outcome Reporting
preset 时 Fail Closed。请通过 `RinIds.Create("request")` 和
`RinIds.Create("event")` 生成一次稳定 ID，将其随操作持久化，并在每次 exact
retry 中复用。

与 OpenAPI 对齐的模型和强类型 overload 覆盖权威 create/propose/action-report
流程。`MutationResult` 与 `ProposalResult` 会通过 `AdditiveFields` 保留未来
新增的响应字段。

`WorkflowCoordinator` 把兼容保留的 `ProposalAttemptCoordinator` 与
`OutcomeOutbox` 组合为 `BeginAsync`、`ResumePendingWorkAsync`、
`ApplyAndEnqueueOutcomeAsync` 和 `DrainOutboxAsync`。接入方提供
`IWorkflowStore` 与已校验的 `HostDurability`。幂等 Apply 会收到稳定
Operation ID；只有 `transactional-action` 才把
`IProposalAttemptStore.SettleAsync` 当作一个游戏事务调用。只有普通成功或
目标 Session 的完整 `MutationResult` 后才能确认 Outbox 项：Revision 必须为正的
JSON-safe 整数，Head 必须为小写 SHA-256，Duplicate 必须显式为布尔值；缺失、
不完整或串线的 Session ACK 会在调用 Store 前 fail closed。错误会保留精确 Report，
绝不把它转换为 Observation。SDK 不提供会误用于生产的内存默认实现。参见
[宿主持久保证分级](../../docs/host-durability.zh-CN.md)。

`OpaqueSnapshotPersistence` 通过 `IOpaqueSnapshotStore` 保存有界 JSON 字节，
并加载完整 `JsonElement`，保留当前 SDK 版本未知的新增 Member。

构建并运行源码测试：

```bash
dotnet run --project sdk/csharp/Rin.Client.Tests/Rin.Client.Tests.csproj
dotnet build sdk/csharp/Rin.Client/Rin.Client.csproj \
  -p:RinTargetFramework=netstandard2.0
```

Unity 和 BepInEx 调用方必须在渲染循环外 `await`，验证结果后再切回 Unity
主线程操作 GameObject。

Session Transfer 只在 .NET 6+ Target 提供。.NET Standard 2.0 兼容构建会
明确排除该功能，因为旧 Unity Stream API 无法提供相同的有界异步契约。
它使用调用方拥有的 stream，不会缓冲完整 lineage。Transfer 独立默认 2 分钟
deadline；可配置 `RinClientOptions.TransferTimeout`，无需放宽普通请求的 5 秒
deadline：

```csharp
var request = new
{
    protocol_version = RinClient.ProtocolVersion,
    session_id = "session.example",
};

await using (var output = File.Create("session.ndjson"))
{
    await rin.ExportSessionAsync(request, output);
}

await using (var input = File.OpenRead("session.ndjson"))
{
    await rin.ImportSessionAsync(
        input,
        new RinBinding(
            "game.example",
            "base",
            "1",
            "trusted-build-hash"));
}
```

Client 不会关闭任一 stream。Export 只有收到合法的终止 `complete` frame
才成功；终止 error、截断、顺序错误与超限 frame 都会抛出异常。Import 通过
独立可信 header 发送 Binding。

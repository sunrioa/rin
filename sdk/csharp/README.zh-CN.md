# Rin C# SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 .NET 6+ 的无第三方依赖异步客户端。

`Rin.Client` 只使用 `HttpClient` 和 `System.Text.Json`。在插件或游戏生命周期
内复用一个 Client。

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

能力协商会在 Runtime 不是 `rin.protocol/v1` 或不支持权威 Outcome Reporting
preset 时 Fail Closed。请通过 `RinIds.Create("request")` 和
`RinIds.Create("event")` 生成一次稳定 ID，将其随操作持久化，并在每次 exact
retry 中复用。

与 OpenAPI 对齐的模型和强类型 overload 覆盖权威 create/propose/commit
流程。`MutationResult` 与 `ProposalResult` 会通过 `AdditiveFields` 保留未来
新增的响应字段。

`ProposalAttemptCoordinator` 与 `OutcomeOutbox` 实现协议指南中的崩溃安全
权威流程。请把 `IProposalAttemptStore.SettleAsync` 实现为一个游戏事务：
应用效果、写入 Applied Marker 与完整 Commit Outbox 项，并删除 Attempt。
`IOutcomeOutboxStore` 必须使用持久存储；只有普通成功或明确 duplicate Commit
成功后才能确认项目。SDK 不提供会误用于生产的内存默认实现。

`OpaqueSnapshotPersistence` 通过 `IOpaqueSnapshotStore` 保存有界 JSON 字节，
并加载完整 `JsonElement`，保留当前 SDK 版本未知的新增 Member。

构建并运行源码测试：

```bash
dotnet run --project sdk/csharp/Rin.Client.Tests/Rin.Client.Tests.csproj
```

Unity 和 BepInEx 调用方必须在渲染循环外 `await`，验证结果后再切回 Unity
主线程操作 GameObject。

Session Transfer 使用调用方拥有的 stream，不会缓冲完整 lineage：

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

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

var health = await rin.HealthAsync();
Console.WriteLine(health.GetProperty("status").GetString());
```

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

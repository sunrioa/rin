# Rin C# SDK

[English](README.md) | [简体中文](README.zh-CN.md)

`Rin.Client` 面向 .NET 6+，只使用 `HttpClient` 和 `System.Text.Json`。
在插件或游戏生命周期内复用一个 Client。

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

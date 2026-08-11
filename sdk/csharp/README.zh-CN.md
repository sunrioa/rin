# Rin C# Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

`Rin.Client` 是 source-first 的 `rin.control/v2` 客户端，目标为 .NET 6 和
.NET Standard 2.0。当前仓库没有承诺已发布 NuGet 包；请直接引用
`Rin.Client/Rin.Client.csproj` 或固定源码 Revision。

```csharp
using Rin.Client;

using var control = new RinControlClient(new RinControlClientOptions
{
    Token = Environment.GetEnvironmentVariable("RIN_CONTROL_TOKEN")!,
});

var info = await control.InfoAsync();
var worlds = await control.ListWorldsAsync();
```

方法返回 `JsonElement`，请求参数使用匿名对象或普通 DTO。默认地址是
`http://127.0.0.1:7375`；客户端禁止 Redirect，并限制响应体、JSON 深度、超时
和 JavaScript 安全整数范围。

所有异步方法接受 `CancellationToken`。取消或超时只说明结果未知，不能据此报告
游戏没有执行；应重新查询同一个 Operation，直到得到权威终态。

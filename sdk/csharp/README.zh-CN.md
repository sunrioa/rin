# Rin C# Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

`Rin.Client` 是 source-first 的 `rin.control/v2` 客户端，目标为 .NET 6 和
.NET Standard 2.0。当前仓库没有承诺已发布 NuGet 包；请直接引用
`Rin.Client/Rin.Client.csproj` 或固定源码 Revision。

同一客户端也通过原始 JSON 方法提供固定的 `/plans/v1/*` 任务计划接口。

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
`http://127.0.0.1:7375`；客户端禁止 Redirect，并限制响应体大小和超时。请求
Payload 还受 JSON 深度和 JavaScript 安全整数范围约束。

所有异步方法接受 `CancellationToken`。提交或等待期间发生取消或网络超时，不能证明
游戏没有执行。已知 Operation ID 时应查询该 Operation；未知时只能用相同 Request
和幂等身份精确重试原提交，不能换新身份重发。详见
[Operation 恢复语义](../../docs/operations.zh-CN.md)。

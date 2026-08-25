# Rin C# Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

`Rin.Client` is a source-first `rin.control/v2` client targeting .NET 6 and
.NET Standard 2.0. This repository does not promise a published NuGet package;
reference `Rin.Client/Rin.Client.csproj` directly or pin the source revision.

The same client exposes the fixed `/plans/v1/*` task-plan routes as raw JSON methods.

```csharp
using Rin.Client;

using var control = new RinControlClient(new RinControlClientOptions
{
    Token = Environment.GetEnvironmentVariable("RIN_CONTROL_TOKEN")!,
});

var info = await control.InfoAsync();
var worlds = await control.ListWorldsAsync();
```

Methods return `JsonElement`; inputs may be anonymous objects or ordinary DTOs.
The default endpoint is `http://127.0.0.1:7375`. The client rejects redirects
and bounds response size and timeout. Request payloads are limited by JSON
depth and the JavaScript-safe integer range.

Every asynchronous method accepts a `CancellationToken`. Cancellation or a
network timeout while submitting or waiting does not prove that the game did
not execute. If the Operation ID is known, query that Operation; otherwise,
exactly retry the original submission with the same request and idempotency
identity. Do not submit a new identity. See the
[Operation recovery semantics](../../docs/operations.md).

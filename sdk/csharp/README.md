# Rin C# SDK

`Rin.Client` targets .NET 6+ and uses only `HttpClient` and
`System.Text.Json`. Keep one client for the lifetime of the plugin or game.

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

Build the source project with:

```bash
dotnet run --project sdk/csharp/Rin.Client.Tests/Rin.Client.Tests.csproj
```

Unity and BepInEx callers must await off the render loop, then marshal the
validated result back to Unity's main thread before touching game objects.

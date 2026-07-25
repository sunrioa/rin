# Rin C# SDK

[English](README.md) | [简体中文](README.zh-CN.md)

A dependency-free asynchronous client for .NET 6+.

`Rin.Client` uses only `HttpClient` and `System.Text.Json`. Keep one client for
the lifetime of the plugin or game.

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

Capability negotiation fails closed unless the Runtime speaks
`rin.protocol/v1` and supports the authoritative outcome-reporting preset.
Create stable identities once with `RinIds.Create("request")` and
`RinIds.Create("event")`, persist them with the operation, and reuse them for
every exact retry.

OpenAPI-aligned models and typed overloads cover the authoritative
create/propose/commit path. `MutationResult` and `ProposalResult` retain
additive response fields through `AdditiveFields`.

`WorkflowCoordinator` combines the compatible `ProposalAttemptCoordinator` and
`OutcomeOutbox` primitives behind `BeginAsync`, `ResumePendingWorkAsync`,
`ApplyAndEnqueueOutcomeAsync`, and `DrainOutboxAsync`. Supply an
`IWorkflowStore` and validated `HostCapabilities`. An idempotent apply receives
the stable operation ID; only `transactional-action` calls
`IProposalAttemptStore.SettleAsync` as one game transaction. Entries are
acknowledged only after normal or explicit duplicate Commit success. The SDK
does not ship an in-memory production default. See
[Host capability profiles](../../docs/host-capability-profiles.md).

`OpaqueSnapshotPersistence` stores bounded JSON bytes through
`IOpaqueSnapshotStore` and loads a complete `JsonElement`, preserving additive
members unknown to this SDK version.

Build the source project with:

```bash
dotnet run --project sdk/csharp/Rin.Client.Tests/Rin.Client.Tests.csproj
```

Unity and BepInEx callers must await off the render loop, then marshal the
validated result back to Unity's main thread before touching game objects.

Session Transfer uses caller-owned streams and never buffers the complete
lineage:

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

The client does not close either stream. Export succeeds only after a valid
terminal `complete` frame; terminal errors, truncation, invalid order, and
oversized frames throw. Import sends the Binding in independent trusted
headers.

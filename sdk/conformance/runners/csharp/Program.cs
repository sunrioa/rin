using System.Text.Json;
using Rin.Client;

static string RequiredEnvironment(string name) =>
    Environment.GetEnvironmentVariable(name)
    ?? throw new InvalidOperationException($"{name} is required");

var body = JsonSerializer.Deserialize<JsonElement>(
    RequiredEnvironment("RIN_SDK_CORPUS_BODY"));
var token = RequiredEnvironment("RIN_SDK_CORPUS_TOKEN");
using var client = new RinClient(new RinClientOptions
{
    BaseUrl = RequiredEnvironment("RIN_SDK_CORPUS_BASE_URL"),
    Token = token,
});
var health = await client.HealthAsync();
if (health.GetProperty("protocol_version").GetString() != RinClient.ProtocolVersion)
{
    throw new InvalidOperationException("C# SDK received an invalid health response");
}
var first = await client.CreateSessionAsync(body);
var retry = await client.CreateSessionAsync(body);
if (first.GetProperty("duplicate").GetBoolean() ||
    !retry.GetProperty("duplicate").GetBoolean() ||
    first.GetProperty("revision").GetInt64() != retry.GetProperty("revision").GetInt64() ||
    first.GetProperty("head_hash").GetString() != retry.GetProperty("head_hash").GetString())
{
    throw new InvalidOperationException("C# SDK exact retry semantics changed");
}

using var slow = new RinClient(new RinClientOptions
{
    BaseUrl = RequiredEnvironment("RIN_SDK_CORPUS_SLOW_URL"),
    Token = token,
    Timeout = TimeSpan.FromMilliseconds(50),
});
try
{
    await slow.CreateSessionAsync(body);
    throw new InvalidOperationException("C# SDK did not enforce its network timeout");
}
catch (RinTransportException error) when (error.Code == "transport_timeout")
{
}

Console.WriteLine("C# SDK live Sidecar corpus passed");

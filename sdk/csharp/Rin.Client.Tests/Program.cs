using System.Net;
using System.Text;
using System.Text.Json;
using Rin.Client;

await ControlClientTests.RunAsync();
Console.WriteLine("C# Control V2 SDK tests passed.");

static class ControlClientTests
{
    private const string Token = "control-fixture-token-32-bytes!!";

    private sealed record RequestCase(
        Func<Task<JsonElement>> Call,
        HttpMethod Method,
        string Path,
        JsonElement? Input);

    internal static async Task RunAsync()
    {
        Require(RinControlClient.ClientVersion == "0.7.0", "client version drifted");
        using var fixture = JsonDocument.Parse(File.ReadAllText(ControlFixturePath()));
        var root = fixture.RootElement;
        Require(
            root.GetProperty("contract_version").GetString() ==
            RinControlClient.ContractVersion,
            "shared Control fixture has the wrong contract");

        var handler = new ControlRecordingHandler();
        using var client = new RinControlClient(
            new RinControlClientOptions { Token = Token },
            handler);
        var world = root.GetProperty("world_target");
        var actor = root.GetProperty("actor_target");
        var operation = root.GetProperty("operation_target");
        var empty = JsonSerializer.SerializeToElement(new Dictionary<string, object>());
        var requests = new[]
        {
            new RequestCase(() => client.InfoAsync(), HttpMethod.Get, "/control/v2/info", null),
            new RequestCase(() => client.ListWorldsAsync(), HttpMethod.Post, "/control/v2/worlds", empty),
            new RequestCase(() => client.ListActorsAsync(world), HttpMethod.Post, "/control/v2/actors", world),
            new RequestCase(() => client.GetActorAsync(actor), HttpMethod.Post, "/control/v2/actor", actor),
            new RequestCase(() => client.WaitActorAsync(root.GetProperty("wait_actor")), HttpMethod.Post, "/control/v2/wait-actor", root.GetProperty("wait_actor")),
            new RequestCase(() => client.ObserveActorAsync(actor), HttpMethod.Post, "/control/v2/observe", actor),
            new RequestCase(() => client.ListCapabilitiesAsync(actor), HttpMethod.Post, "/control/v2/capabilities", actor),
            new RequestCase(() => client.DescribeCapabilityAsync(root.GetProperty("describe_capability")), HttpMethod.Post, "/control/v2/capability", root.GetProperty("describe_capability")),
            new RequestCase(() => client.AcquireControllerAsync(root.GetProperty("acquire_controller")), HttpMethod.Post, "/control/v2/controllers/acquire", root.GetProperty("acquire_controller")),
            new RequestCase(() => client.RenewControllerAsync(root.GetProperty("renew_controller")), HttpMethod.Post, "/control/v2/controllers/renew", root.GetProperty("renew_controller")),
            new RequestCase(() => client.ReleaseControllerAsync(root.GetProperty("release_controller")), HttpMethod.Post, "/control/v2/controllers/release", root.GetProperty("release_controller")),
            new RequestCase(() => client.GetControllerAsync(actor), HttpMethod.Post, "/control/v2/controllers/get", actor),
            new RequestCase(() => client.SubmitActionAsync(root.GetProperty("submit_action")), HttpMethod.Post, "/control/v2/actions/submit", root.GetProperty("submit_action")),
            new RequestCase(() => client.ConfirmActionAsync(operation), HttpMethod.Post, "/control/v2/actions/confirm", operation),
            new RequestCase(() => client.GetOperationAsync(operation), HttpMethod.Post, "/control/v2/operations/get", operation),
            new RequestCase(() => client.WaitOperationAsync(root.GetProperty("wait_operation")), HttpMethod.Post, "/control/v2/operations/wait", root.GetProperty("wait_operation")),
            new RequestCase(() => client.GetTaskTimelineAsync(root.GetProperty("task_timeline")), HttpMethod.Post, "/control/v2/tasks/timeline/get", root.GetProperty("task_timeline")),
            new RequestCase(() => client.WaitTaskTimelineAsync(root.GetProperty("wait_task_timeline")), HttpMethod.Post, "/control/v2/tasks/timeline/wait", root.GetProperty("wait_task_timeline")),
            new RequestCase(() => client.CancelOperationAsync(operation), HttpMethod.Post, "/control/v2/operations/cancel", operation),
            new RequestCase(() => client.SetEmergencyStopAsync(root.GetProperty("emergency_stop")), HttpMethod.Post, "/control/v2/emergency-stop", root.GetProperty("emergency_stop")),
        };

        foreach (var request in requests)
        {
            var response = await request.Call();
            Require(handler.Method == request.Method, $"method changed for {request.Path}");
            Require(handler.Path == request.Path, $"route changed for {request.Path}");
            Require(handler.Authorization == "Bearer " + Token, "bearer token was not sent");
            Require(
                handler.UserAgent == "rin-control-csharp/" + RinControlClient.ClientVersion,
                "user agent drifted");
            if (request.Input is JsonElement expected)
            {
                using var body = JsonDocument.Parse(handler.Body);
                Require(JsonEquivalent(body.RootElement, expected), $"body changed for {request.Path}");
            }
            if (request.Path is "/control/v2/worlds" or "/control/v2/actors")
            {
                Require(response.ValueKind == JsonValueKind.Array, $"list response changed for {request.Path}");
            }
        }

        await RequireModeAsync<RinProtocolException>(client, handler, actor, ControlResponseMode.ContractMismatch, "control_contract_mismatch", info: true);
        await RequireModeAsync<RinApiException>(client, handler, actor, ControlResponseMode.ApiError, "stale");
        await RequireModeAsync<RinProtocolException>(client, handler, actor, ControlResponseMode.WrongContentType, "invalid_response");
        await RequireModeAsync<RinTransportException>(client, handler, actor, ControlResponseMode.Redirect, "redirect_rejected");
        await RequireModeAsync<RinProtocolException>(client, handler, actor, ControlResponseMode.InvalidJson, "invalid_response");
        await RequireModeAsync<RinProtocolException>(client, handler, actor, ControlResponseMode.Scalar, "invalid_response");
        handler.Mode = ControlResponseMode.Normal;

        using var bounded = new RinControlClient(
            new RinControlClientOptions { Token = Token, MaxResponseBytes = 1024 },
            new ControlRecordingHandler { Mode = ControlResponseMode.Oversized });
        await RequireCodeAsync<RinProtocolException>(() => bounded.GetActorAsync(actor), "response_too_large");

        using var slow = new RinControlClient(
            new RinControlClientOptions { Token = Token, Timeout = TimeSpan.FromMilliseconds(50) },
            new ControlRecordingHandler { Mode = ControlResponseMode.Slow });
        await RequireCodeAsync<RinTransportException>(() => slow.GetActorAsync(actor), "transport_timeout");

        using var callerCanceled = new CancellationTokenSource();
        callerCanceled.Cancel();
        await RequireThrowsAsync<OperationCanceledException>(
            () => client.GetActorAsync(actor, callerCanceled.Token));

        var arrayPayload = JsonSerializer.SerializeToElement(new[] { 1, 2 });
        await RequireCodeAsync<RinProtocolException>(() => client.GetActorAsync(arrayPayload), "invalid_request");
        await RequireCodeAsync<RinProtocolException>(
            () => client.GetActorAsync(new { unsafe_integer = long.MaxValue }),
            "invalid_request");

        RequireCode<RinConfigurationException>(
            () => new RinControlClient(new RinControlClientOptions { Token = "short" }),
            "invalid_token");
        foreach (var baseUrl in new[]
        {
            "https://127.0.0.1:7375",
            "http://example.com:7375",
            "http://127.0.0.1",
            "http://127.0.0.1:7375/path",
            "http://user@127.0.0.1:7375",
        })
        {
            RequireCode<RinConfigurationException>(
                () => new RinControlClient(new RinControlClientOptions { BaseUrl = baseUrl, Token = Token }),
                "invalid_base_url");
        }
    }

    private static async Task RequireModeAsync<TException>(
        RinControlClient client,
        ControlRecordingHandler handler,
        JsonElement actor,
        ControlResponseMode mode,
        string code,
        bool info = false)
        where TException : RinException
    {
        handler.Mode = mode;
        await RequireCodeAsync<TException>(
            info ? () => client.InfoAsync() : () => client.GetActorAsync(actor),
            code);
    }

    private static async Task RequireCodeAsync<TException>(
        Func<Task<JsonElement>> operation,
        string code)
        where TException : RinException
    {
        try
        {
            await operation();
            throw new InvalidOperationException($"expected {code}");
        }
        catch (TException exception)
        {
            Require(exception.Code == code, $"unexpected Control error: {exception.Code}");
        }
    }

    private static void RequireCode<TException>(Action operation, string code)
        where TException : RinException
    {
        try
        {
            operation();
            throw new InvalidOperationException($"expected {code}");
        }
        catch (TException exception)
        {
            Require(exception.Code == code, $"unexpected Control error: {exception.Code}");
        }
    }

    private static async Task RequireThrowsAsync<TException>(Func<Task<JsonElement>> operation)
        where TException : Exception
    {
        try
        {
            await operation();
            throw new InvalidOperationException($"expected {typeof(TException).Name}");
        }
        catch (TException)
        {
        }
    }

    private static bool JsonEquivalent(JsonElement left, JsonElement right)
    {
        if (left.ValueKind != right.ValueKind) return false;
        if (left.ValueKind == JsonValueKind.Object)
        {
            var rightProperties = right.EnumerateObject().ToDictionary(item => item.Name, item => item.Value);
            var leftProperties = left.EnumerateObject().ToArray();
            return leftProperties.Length == rightProperties.Count &&
                leftProperties.All(item => rightProperties.TryGetValue(item.Name, out var value) && JsonEquivalent(item.Value, value));
        }
        if (left.ValueKind == JsonValueKind.Array)
        {
            var leftItems = left.EnumerateArray().ToArray();
            var rightItems = right.EnumerateArray().ToArray();
            return leftItems.Length == rightItems.Length &&
                leftItems.Zip(rightItems).All(pair => JsonEquivalent(pair.First, pair.Second));
        }
        return left.GetRawText() == right.GetRawText();
    }

    private static string ControlFixturePath()
    {
        foreach (var start in new[] { Directory.GetCurrentDirectory(), AppContext.BaseDirectory })
        {
            for (DirectoryInfo? directory = new(start); directory is not null; directory = directory.Parent)
            {
                var candidate = Path.Combine(directory.FullName, "api", "control-v2-fixtures.json");
                if (File.Exists(candidate)) return candidate;
            }
        }
        throw new FileNotFoundException("cannot locate api/control-v2-fixtures.json");
    }

    private static void Require(bool condition, string message)
    {
        if (!condition) throw new InvalidOperationException(message);
    }
}

enum ControlResponseMode
{
    Normal,
    ContractMismatch,
    ApiError,
    WrongContentType,
    Redirect,
    Oversized,
    InvalidJson,
    Scalar,
    Slow,
}

sealed class ControlRecordingHandler : HttpMessageHandler
{
    public ControlResponseMode Mode { get; set; }
    public HttpMethod? Method { get; private set; }
    public string Path { get; private set; } = string.Empty;
    public string Authorization { get; private set; } = string.Empty;
    public string UserAgent { get; private set; } = string.Empty;
    public string Body { get; private set; } = string.Empty;

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request,
        CancellationToken cancellationToken)
    {
        Method = request.Method;
        Path = request.RequestUri?.AbsolutePath ?? string.Empty;
        Authorization = request.Headers.Authorization?.ToString() ?? string.Empty;
        UserAgent = request.Headers.UserAgent.ToString();
        Body = request.Content is null
            ? string.Empty
            : await request.Content.ReadAsStringAsync(cancellationToken);
        if (Mode == ControlResponseMode.Slow)
        {
            await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
        }

        var status = Mode switch
        {
            ControlResponseMode.ApiError => HttpStatusCode.Conflict,
            ControlResponseMode.Redirect => HttpStatusCode.Found,
            _ => HttpStatusCode.OK,
        };
        var responseBody = Mode switch
        {
            ControlResponseMode.ContractMismatch => "{\"contract_version\":\"rin.control/v1\"}",
            ControlResponseMode.ApiError => "{\"code\":\"stale\",\"error\":\"world changed\"}",
            ControlResponseMode.Oversized => "{\"padding\":\"" + new string('x', 2048) + "\"}",
            ControlResponseMode.InvalidJson => "{",
            ControlResponseMode.Scalar => "true",
            _ when Path == "/control/v2/info" =>
                "{\"contract_version\":\"rin.control/v2\",\"principal\":{\"id\":\"principal.fixture\"}}",
            _ when Path is "/control/v2/worlds" or "/control/v2/actors" => "[{\"id\":\"fixture\"}]",
            _ => "{\"status\":\"ok\"}",
        };
        var content = new ByteArrayContent(Encoding.UTF8.GetBytes(responseBody));
        content.Headers.ContentType = new System.Net.Http.Headers.MediaTypeHeaderValue(
            Mode == ControlResponseMode.WrongContentType ? "text/plain" : "application/json");
        return new HttpResponseMessage(status) { Content = content };
    }
}

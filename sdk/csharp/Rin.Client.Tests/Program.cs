using System.Net;
using System.Text;
using System.Text.Json;
using Rin.Client;

Require(
    new RinClientOptions().MaxResponseBytes == 32 * 1024 * 1024,
    "default response limit does not match the inline transport budget");
Require(RinClient.ClientVersion == "0.6.0", "client version projection is stale");

var handler = new RecordingHandler();
using var client = new RinClient(new RinClientOptions { Token = "fixture" }, handler);
var payload = new Dictionary<string, object?>
{
    ["protocol_version"] = RinClient.ProtocolVersion,
    ["request_id"] = "request.fixture",
    ["utf8"] = "雨",
};
var cases = new (string Name, Func<Task<JsonElement>> Call, HttpMethod Method, string Path)[]
{
    ("health", () => client.HealthAsync(), HttpMethod.Get, "/health"),
    ("create_session", () => client.CreateSessionAsync(payload), HttpMethod.Post, "/v1/session/create"),
    ("observe", () => client.ObserveAsync(payload), HttpMethod.Post, "/v1/session/observe"),
    ("propose", () => client.ProposeAsync(payload), HttpMethod.Post, "/v1/agent/propose"),
    ("submit_proposal_job", () => client.SubmitProposalJobAsync(payload), HttpMethod.Post, "/v1/jobs/propose"),
    ("get_proposal_job", () => client.GetProposalJobAsync("job.fixture"), HttpMethod.Get, "/v1/jobs/job.fixture"),
    ("cancel_proposal_job", () => client.CancelProposalJobAsync("job.fixture"), HttpMethod.Delete, "/v1/jobs/job.fixture"),
    ("submit_generation_job", () => client.SubmitGenerationJobAsync(payload), HttpMethod.Post, "/v1/generation/jobs"),
    ("get_generation_job", () => client.GetGenerationJobAsync("job.fixture"), HttpMethod.Get, "/v1/generation/jobs/job.fixture"),
    ("cancel_generation_job", () => client.CancelGenerationJobAsync("job.fixture"), HttpMethod.Delete, "/v1/generation/jobs/job.fixture"),
    ("commit", () => client.CommitAsync(payload), HttpMethod.Post, "/v1/action/commit"),
    ("commit_batch", () => client.CommitBatchAsync(payload), HttpMethod.Post, "/v1/action/commit-batch"),
    ("set_actor_activity", () => client.SetActorActivityAsync(payload), HttpMethod.Post, "/v1/session/activity"),
    ("arbitrate", () => client.ArbitrateAsync(payload), HttpMethod.Post, "/v1/world/arbitrate"),
    ("state", () => client.StateAsync(payload), HttpMethod.Post, "/v1/session/get"),
    ("snapshot", () => client.SnapshotAsync(payload), HttpMethod.Post, "/v1/session/snapshot"),
    ("restore", () => client.RestoreAsync(payload), HttpMethod.Post, "/v1/session/restore"),
    ("timeline", () => client.TimelineAsync(payload), HttpMethod.Post, "/v1/session/timeline"),
    ("replay", () => client.ReplayAsync(payload), HttpMethod.Post, "/v1/session/replay"),
    ("due_agents", () => client.DueAgentsAsync(payload), HttpMethod.Post, "/v1/scheduler/due"),
};

var observedRoutes = new List<string>();
foreach (var test in cases)
{
    var result = await test.Call();
    Require(handler.Method == test.Method, "wrong method for " + test.Path);
    Require(handler.Path == test.Path, "wrong path for " + test.Path);
    Require(handler.Authorization == "Bearer fixture", "missing bearer token");
    Require(handler.UserAgent == "rin-csharp/" + RinClient.ClientVersion, "wrong user agent");
    Require(result.GetProperty("status").GetString() == "ok", "response envelope was not decoded");
    if (test.Method == HttpMethod.Post)
    {
        using var sent = JsonDocument.Parse(handler.Body);
        Require(
            sent.RootElement.GetProperty("protocol_version").GetString() == RinClient.ProtocolVersion,
            "request protocol_version was not serialized");
        Require(
            sent.RootElement.GetProperty("request_id").GetString() == "request.fixture",
            "request_id was not serialized");
        Require(sent.RootElement.GetProperty("utf8").GetString() == "雨", "UTF-8 request text changed");
    }
    else
    {
        Require(handler.Body == string.Empty, "bodyless route sent a request body");
    }
    observedRoutes.Add(RouteKey(
        test.Name,
        handler.Method?.Method ?? string.Empty,
        handler.Path.Replace("job.fixture", "{job_id}"),
        (int)handler.Status));
}
var expectedRoutes = ContractRouteKeys();
observedRoutes.Sort(StringComparer.Ordinal);
Require(
    observedRoutes.SequenceEqual(expectedRoutes, StringComparer.Ordinal),
    "actual SDK request method/path/status set differs from sdk/conformance/routes.json");

await client.CommitAsync(new Dictionary<string, object?> { ["accepted"] = false });
using (var sent = JsonDocument.Parse(handler.Body))
{
    Require(
        sent.RootElement.TryGetProperty("accepted", out var accepted) &&
        accepted.ValueKind == JsonValueKind.False,
        "commit accepted=false was omitted or changed");
}
await client.CommitBatchAsync(new Dictionary<string, object?>
{
    ["items"] = new object?[] { new Dictionary<string, object?> { ["accepted"] = false } },
});
using (var sent = JsonDocument.Parse(handler.Body))
{
    var item = sent.RootElement.GetProperty("items").EnumerateArray().Single();
    Require(
        item.TryGetProperty("accepted", out var accepted) &&
        accepted.ValueKind == JsonValueKind.False,
        "batch accepted=false was omitted or changed");
}

var cyclicPayload = new Dictionary<string, object?>();
cyclicPayload["self"] = cyclicPayload;
object deepPayload = "leaf";
for (var depth = 0; depth < 66; depth++) deepPayload = new object?[] { deepPayload };
var invalidPayloads = new object[]
{
    new Dictionary<string, object?>
    {
        ["nested"] = new object?[] { new Dictionary<string, object?> { ["unsafe"] = 9_007_199_254_740_992L } },
    },
    new Dictionary<string, object?> { ["nested"] = double.NaN },
    new Dictionary<string, object?> { ["nested"] = double.PositiveInfinity },
    cyclicPayload,
    new Dictionary<string, object?> { ["nested"] = deepPayload },
};
var requestsBeforeInvalidPayloads = handler.RequestCount;
foreach (var invalidPayload in invalidPayloads)
{
    try
    {
        await client.CommitAsync(invalidPayload);
        throw new InvalidOperationException("invalid JSON payload was accepted");
    }
    catch (RinProtocolException exception)
    {
        Require(exception.Code == "invalid_request", "invalid JSON payload returned the wrong error");
    }
}
Require(
    handler.RequestCount == requestsBeforeInvalidPayloads,
    "invalid JSON payload reached the transport");

var apiErrorHandler = new RecordingHandler
{
    ForcedStatus = HttpStatusCode.BadRequest,
    ResponseBodyFactory = _ =>
        "{\"ok\":false,\"error\":{\"code\":\"invalid_request\",\"message\":\"safe\",\"field\":\"actor_id\"}}",
};
using (var apiErrorClient = new RinClient(new RinClientOptions(), apiErrorHandler))
{
    try
    {
        await apiErrorClient.HealthAsync();
        throw new InvalidOperationException("API error envelope was accepted");
    }
    catch (RinApiException exception)
    {
        Require(exception.Status == 400, "API error status changed");
        Require(exception.Code == "invalid_request", "API error code changed");
        Require(exception.Field == "actor_id", "API error field changed");
    }
}

RequireThrows<RinConfigurationException>(() => new RinClient(new RinClientOptions
{
    BaseUrl = "http://models.example",
    Token = "fixture",
}), "remote HTTP origin was accepted");
RequireThrows<RinConfigurationException>(() => new RinClient(new RinClientOptions
{
    BaseUrl = "https://models.example",
}), "remote origin without token was accepted");
RequireThrows<RinConfigurationException>(
    () => client.GetProposalJobAsync("\u4f5c\u4e1a"),
    "Unicode path ID was accepted");

var oversized = new RecordingHandler { DeclaredLength = 2048 };
using var limited = new RinClient(new RinClientOptions { MaxResponseBytes = 1024 }, oversized);
try
{
    await limited.HealthAsync();
    throw new InvalidOperationException("oversized response was accepted");
}
catch (RinProtocolException exception)
{
    Require(exception.Code == "response_too_large", "wrong response limit error");
}

var slow = new RecordingHandler { ContentFactory = () => new StreamContent(new SlowStream()) };
using var impatient = new RinClient(new RinClientOptions { Timeout = TimeSpan.FromMilliseconds(50) }, slow);
try
{
    await impatient.HealthAsync();
    throw new InvalidOperationException("slow response exceeded the request deadline");
}
catch (RinTransportException exception)
{
    Require(exception.Code == "transport_timeout", "wrong timeout error");
}

var proposalRace = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? ProposalJobBody(
            "succeeded",
            ",\"proposal\":{\"id\":\"proposal.race\",\"session_id\":\"session.fixture\",\"request_id\":\"request.fixture\",\"actor_id\":\"actor.fixture\",\"tick\":7}")
        : ProposalJobBody("running"),
};
using var proposalRaceClient = new RinClient(new RinClientOptions(), proposalRace);
var proposalRaceJob = await proposalRaceClient.WaitForProposalAsync(
    "job.fixture",
    TimeSpan.FromMilliseconds(50),
    TimeSpan.FromMilliseconds(10));
Require(
    proposalRaceJob.GetProperty("proposal").GetProperty("id").GetString() == "proposal.race",
    "proposal completion returned by cancellation was discarded");

var generationRace = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? GenerationJobBody("succeeded", ",\"result\":{\"content\":\"finished at the deadline\"}")
        : GenerationJobBody("queued"),
};
using var generationRaceClient = new RinClient(new RinClientOptions(), generationRace);
var generationRaceJob = await generationRaceClient.WaitForGenerationAsync(
    "job.fixture",
    TimeSpan.FromMilliseconds(50),
    TimeSpan.FromMilliseconds(10));
Require(
    generationRaceJob.GetProperty("result").GetProperty("content").GetString() == "finished at the deadline",
    "generation completion returned by cancellation was discarded");

var terminalCancel = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? ProposalJobBody("stale", ",\"error\":{\"code\":\"proposal_stale\",\"message\":\"World changed\"}")
        : ProposalJobBody("running"),
};
using var terminalCancelClient = new RinClient(new RinClientOptions(), terminalCancel);
try
{
    await terminalCancelClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromMilliseconds(50),
        TimeSpan.FromMilliseconds(10));
    throw new InvalidOperationException("terminal cancellation result was discarded");
}
catch (RinApiException exception)
{
    Require(exception.Code == "proposal_stale", "terminal cancellation result became job_timeout");
}

var canceledDuringGet = new CancellationReconciliationHandler(
    ProposalJobBody(
        "succeeded",
        ",\"proposal\":{\"id\":\"proposal.after-cancel\",\"session_id\":\"session.fixture\",\"request_id\":\"request.fixture\",\"actor_id\":\"actor.fixture\",\"tick\":8}"),
    blockGetUntilCanceled: true);
using var canceledDuringGetClient = new RinClient(new RinClientOptions(), canceledDuringGet);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = canceledDuringGetClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromMilliseconds(10),
        callerCancellation.Token);
    await canceledDuringGet.GetStarted;
    callerCancellation.Cancel();
    var reconciled = await wait;
    Require(canceledDuringGet.DeleteCount == 1, "caller cancellation during GET did not issue DELETE");
    Require(
        reconciled.GetProperty("proposal").GetProperty("id").GetString() == "proposal.after-cancel",
        "proposal raced with caller cancellation was discarded");
}

var confirmedCallerCancellation = new CancellationReconciliationHandler(
    ProposalJobBody("canceled"));
using var confirmedCallerCancellationClient = new RinClient(new RinClientOptions(), confirmedCallerCancellation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = confirmedCallerCancellationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await confirmedCallerCancellation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("confirmed caller cancellation did not remain canceled");
    }
    catch (OperationCanceledException)
    {
        Require(callerCancellation.IsCancellationRequested, "wrong cancellation was propagated");
    }
    Require(confirmedCallerCancellation.DeleteCount == 1, "caller cancellation during delay did not issue DELETE");
}

var unconfirmedCallerCancellation = new CancellationReconciliationHandler(
    ProposalJobBody("running"));
using var unconfirmedCallerCancellationClient = new RinClient(new RinClientOptions(), unconfirmedCallerCancellation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = unconfirmedCallerCancellationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await unconfirmedCallerCancellation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("unconfirmed caller cancellation was treated as safe");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "job_outcome_unknown", "unresolved DELETE returned the wrong error");
    }
}

var staleCallerReconciliation = new CancellationReconciliationHandler(
    ProposalJobBody("stale", ",\"error\":{\"code\":\"proposal_stale\",\"message\":\"World changed\"}"));
using var staleCallerReconciliationClient = new RinClient(new RinClientOptions(), staleCallerReconciliation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = staleCallerReconciliationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await staleCallerReconciliation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("stale cancellation reconciliation was discarded");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "proposal_stale", "stale DELETE terminal result was not propagated");
    }
}

var failedCallerReconciliation = new CancellationReconciliationHandler(
    "{\"ok\":false,\"error\":{\"code\":\"cancel_failed\",\"message\":\"Cancellation failed\"}}");
using var failedCallerReconciliationClient = new RinClient(new RinClientOptions(), failedCallerReconciliation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = failedCallerReconciliationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await failedCallerReconciliation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("failed cancellation reconciliation was treated as safe");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "job_cancel_unconfirmed", "failed DELETE returned the wrong error");
    }
}

var malformedCallerReconciliation = new CancellationReconciliationHandler("not-json");
using var malformedCallerReconciliationClient = new RinClient(new RinClientOptions(), malformedCallerReconciliation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = malformedCallerReconciliationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await malformedCallerReconciliation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("malformed cancellation reconciliation was treated as safe");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "job_cancel_unconfirmed", "malformed DELETE returned the wrong error");
    }
}

var crossedGet = new RecordingHandler
{
    ResponseBodyFactory = _ => ProposalJobBody("running", jobId: "job.other"),
};
using var crossedGetClient = new RinClient(new RinClientOptions(), crossedGet);
try
{
    await crossedGetClient.WaitForProposalAsync("job.fixture");
    throw new InvalidOperationException("crossed GET job identity was accepted");
}
catch (RinProtocolException exception)
{
    Require(exception.Code == "invalid_job", "crossed GET returned the wrong error");
}

foreach (var malformedStatus in new[] { "", " canceled ", "canceled\\u0000" })
{
    var malformedStatusGet = new RecordingHandler
    {
        ResponseBodyFactory = _ => ProposalJobBody(malformedStatus),
    };
    using var malformedStatusGetClient = new RinClient(new RinClientOptions(), malformedStatusGet);
    try
    {
        await malformedStatusGetClient.WaitForProposalAsync("job.fixture");
        throw new InvalidOperationException("polling accepted a normalized pseudo-status");
    }
    catch (RinProtocolException exception)
    {
        Require(exception.Code == "invalid_job", "malformed polling status returned the wrong error");
    }
}

foreach (var malformedStatus in new[] { "", " canceled ", "canceled\\u0000" })
{
    var malformedStatusCancellation = new CancellationReconciliationHandler(
        ProposalJobBody(malformedStatus));
    using var malformedStatusCancellationClient =
        new RinClient(new RinClientOptions(), malformedStatusCancellation);
    using var callerCancellation = new CancellationTokenSource();
    var wait = malformedStatusCancellationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await malformedStatusCancellation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("caller cancellation accepted a normalized pseudo-status");
    }
    catch (RinApiException exception)
    {
        Require(
            exception.Code == "job_outcome_unknown",
            "malformed cancellation status returned the wrong error");
    }
}

var malformedDelete = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? ProposalJobBody(
            "succeeded",
            ",\"proposal\":{\"id\":\"proposal.race\",\"session_id\":\"session.fixture\",\"request_id\":\"request.fixture\",\"actor_id\":\"actor.fixture\",\"tick\":9007199254740992}")
        : ProposalJobBody("running"),
};
using var malformedDeleteClient = new RinClient(new RinClientOptions(), malformedDelete);
try
{
    await malformedDeleteClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromMilliseconds(50),
        TimeSpan.FromMilliseconds(10));
    throw new InvalidOperationException("malformed DELETE proposal identity was accepted");
}
catch (RinProtocolException exception)
{
    Require(exception.Code == "invalid_job", "malformed DELETE returned the wrong error");
}

Console.WriteLine("Rin C# SDK tests passed");

static void Require(bool condition, string message)
{
    if (!condition) throw new InvalidOperationException(message);
}

static void RequireThrows<TException>(Action action, string message) where TException : Exception
{
    try
    {
        action();
        throw new InvalidOperationException(message);
    }
    catch (TException)
    {
    }
}

static string RouteKey(string name, string method, string path, int status) =>
    name + " " + method + " " + path + " " + status;

static string[] ContractRouteKeys()
{
    using var document = JsonDocument.Parse(File.ReadAllText(ContractManifestPath()));
    return document.RootElement.GetProperty("operations")
        .EnumerateArray()
        .Select(operation => RouteKey(
            operation.GetProperty("name").GetString() ?? string.Empty,
            operation.GetProperty("method").GetString() ?? string.Empty,
            operation.GetProperty("path").GetString() ?? string.Empty,
            operation.GetProperty("status").GetInt32()))
        .OrderBy(route => route, StringComparer.Ordinal)
        .ToArray();
}

static string ContractManifestPath()
{
    foreach (var start in new[] { Directory.GetCurrentDirectory(), AppContext.BaseDirectory })
    {
        for (DirectoryInfo? directory = new(start); directory is not null; directory = directory.Parent)
        {
            foreach (var relative in new[]
            {
                Path.Combine("sdk", "conformance", "routes.json"),
                Path.Combine("conformance", "routes.json"),
            })
            {
                var candidate = Path.Combine(directory.FullName, relative);
                if (File.Exists(candidate)) return candidate;
            }
        }
    }
    throw new FileNotFoundException("cannot locate sdk/conformance/routes.json");
}

static string ProposalJobBody(
    string status,
    string suffix = "",
    string jobId = "job.fixture",
    string sessionId = "session.fixture",
    string requestId = "request.fixture") =>
    "{\"ok\":true,\"data\":{\"job_id\":\"" + jobId +
    "\",\"session_id\":\"" + sessionId +
    "\",\"request_id\":\"" + requestId +
    "\",\"status\":\"" + status + "\"" + suffix + "}}";

static string GenerationJobBody(
    string status,
    string suffix = "",
    string jobId = "job.fixture",
    string requestId = "generation.fixture") =>
    "{\"ok\":true,\"data\":{\"job_id\":\"" + jobId +
    "\",\"request_id\":\"" + requestId +
    "\",\"status\":\"" + status + "\"" + suffix + "}}";

sealed class RecordingHandler : HttpMessageHandler
{
    public int RequestCount { get; private set; }
    public HttpMethod? Method { get; private set; }
    public string Path { get; private set; } = string.Empty;
    public string Authorization { get; private set; } = string.Empty;
    public string UserAgent { get; private set; } = string.Empty;
    public string Body { get; private set; } = string.Empty;
    public HttpStatusCode Status { get; private set; }
    public long? DeclaredLength { get; init; }
    public HttpStatusCode? ForcedStatus { get; init; }
    public Func<HttpContent>? ContentFactory { get; init; }
    public Func<HttpRequestMessage, string>? ResponseBodyFactory { get; init; }

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request,
        CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        RequestCount++;
        Method = request.Method;
        Path = request.RequestUri?.AbsolutePath ?? string.Empty;
        Authorization = request.Headers.TryGetValues("Authorization", out var values)
            ? values.Single()
            : string.Empty;
        UserAgent = request.Headers.UserAgent.ToString();
        Body = request.Content is null
            ? string.Empty
            : await request.Content.ReadAsStringAsync(cancellationToken);
        var status = ForcedStatus ?? (Path is "/v1/jobs/propose" or "/v1/generation/jobs"
            ? HttpStatusCode.Accepted
            : HttpStatusCode.OK);
        Status = status;
        var responseBodyFactory = ResponseBodyFactory;
        var content = responseBodyFactory is not null
            ? new ByteArrayContent(Encoding.UTF8.GetBytes(responseBodyFactory(request)))
            : ContentFactory?.Invoke() ??
              new ByteArrayContent(Encoding.UTF8.GetBytes("{\"ok\":true,\"data\":{\"status\":\"ok\"}}"));
        if (DeclaredLength.HasValue) content.Headers.ContentLength = DeclaredLength.Value;
        return new HttpResponseMessage(status) { Content = content };
    }
}

sealed class SlowStream : Stream
{
    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => false;
    public override long Length => throw new NotSupportedException();
    public override long Position
    {
        get => throw new NotSupportedException();
        set => throw new NotSupportedException();
    }

    public override void Flush() { }
    public override int Read(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();

    public override async ValueTask<int> ReadAsync(
        Memory<byte> buffer,
        CancellationToken cancellationToken = default)
    {
        await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
        return 0;
    }
}

sealed class CancellationReconciliationHandler : HttpMessageHandler
{
    private readonly string deleteResponseBody;
    private readonly bool blockGetUntilCanceled;
    private readonly TaskCompletionSource<bool> getStarted =
        new(TaskCreationOptions.RunContinuationsAsynchronously);

    public CancellationReconciliationHandler(string deleteResponseBody, bool blockGetUntilCanceled = false)
    {
        this.deleteResponseBody = deleteResponseBody;
        this.blockGetUntilCanceled = blockGetUntilCanceled;
    }

    public Task GetStarted => getStarted.Task;

    public int DeleteCount { get; private set; }

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request,
        CancellationToken cancellationToken)
    {
        if (request.Method == HttpMethod.Delete)
        {
            if (cancellationToken.IsCancellationRequested)
            {
                throw new InvalidOperationException("DELETE reused the canceled caller token");
            }
            DeleteCount++;
            return Response(deleteResponseBody);
        }

        getStarted.TrySetResult(true);
        if (blockGetUntilCanceled)
        {
            await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
        }
        return Response(
            "{\"ok\":true,\"data\":{\"job_id\":\"job.fixture\",\"session_id\":\"session.fixture\"," +
            "\"request_id\":\"request.fixture\",\"status\":\"running\"}}");
    }

    private static HttpResponseMessage Response(string body) =>
        new(HttpStatusCode.OK)
        {
            Content = new ByteArrayContent(Encoding.UTF8.GetBytes(body)),
        };
}

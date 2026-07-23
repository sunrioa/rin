using System.Net;
using System.Text;
using Rin.Client;

var handler = new RecordingHandler();
using var client = new RinClient(new RinClientOptions { Token = "fixture" }, handler);
var payload = new Dictionary<string, object?>();
var cases = new (Func<Task> Call, HttpMethod Method, string Path)[]
{
    (async () => await client.HealthAsync(), HttpMethod.Get, "/health"),
    (async () => await client.CreateSessionAsync(payload), HttpMethod.Post, "/v1/session/create"),
    (async () => await client.ObserveAsync(payload), HttpMethod.Post, "/v1/session/observe"),
    (async () => await client.ProposeAsync(payload), HttpMethod.Post, "/v1/agent/propose"),
    (async () => await client.SubmitProposalJobAsync(payload), HttpMethod.Post, "/v1/jobs/propose"),
    (async () => await client.GetProposalJobAsync("job.fixture"), HttpMethod.Get, "/v1/jobs/job.fixture"),
    (async () => await client.CancelProposalJobAsync("job.fixture"), HttpMethod.Delete, "/v1/jobs/job.fixture"),
    (async () => await client.SubmitGenerationJobAsync(payload), HttpMethod.Post, "/v1/generation/jobs"),
    (async () => await client.GetGenerationJobAsync("job.fixture"), HttpMethod.Get, "/v1/generation/jobs/job.fixture"),
    (async () => await client.CancelGenerationJobAsync("job.fixture"), HttpMethod.Delete, "/v1/generation/jobs/job.fixture"),
    (async () => await client.CommitAsync(payload), HttpMethod.Post, "/v1/action/commit"),
    (async () => await client.CommitBatchAsync(payload), HttpMethod.Post, "/v1/action/commit-batch"),
    (async () => await client.SetActorActivityAsync(payload), HttpMethod.Post, "/v1/session/activity"),
    (async () => await client.ArbitrateAsync(payload), HttpMethod.Post, "/v1/world/arbitrate"),
    (async () => await client.StateAsync(payload), HttpMethod.Post, "/v1/session/get"),
    (async () => await client.SnapshotAsync(payload), HttpMethod.Post, "/v1/session/snapshot"),
    (async () => await client.RestoreAsync(payload), HttpMethod.Post, "/v1/session/restore"),
    (async () => await client.TimelineAsync(payload), HttpMethod.Post, "/v1/session/timeline"),
    (async () => await client.ReplayAsync(payload), HttpMethod.Post, "/v1/session/replay"),
    (async () => await client.DueAgentsAsync(payload), HttpMethod.Post, "/v1/scheduler/due"),
};

foreach (var test in cases)
{
    await test.Call();
    Require(handler.Method == test.Method, "wrong method for " + test.Path);
    Require(handler.Path == test.Path, "wrong path for " + test.Path);
    Require(handler.Authorization == "Bearer fixture", "missing bearer token");
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
            ",\"proposal\":{\"id\":\"proposal.race\",\"session_id\":\"session.fixture\",\"request_id\":\"request.fixture\",\"actor_id\":\"actor.fixture\",\"tick\":1.5}")
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
    public HttpMethod? Method { get; private set; }
    public string Path { get; private set; } = string.Empty;
    public string Authorization { get; private set; } = string.Empty;
    public long? DeclaredLength { get; init; }
    public Func<HttpContent>? ContentFactory { get; init; }
    public Func<HttpRequestMessage, string>? ResponseBodyFactory { get; init; }

    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Method = request.Method;
        Path = request.RequestUri?.AbsolutePath ?? string.Empty;
        Authorization = request.Headers.TryGetValues("Authorization", out var values)
            ? values.Single()
            : string.Empty;
        var status = Path is "/v1/jobs/propose" or "/v1/generation/jobs"
            ? HttpStatusCode.Accepted
            : HttpStatusCode.OK;
        var responseBodyFactory = ResponseBodyFactory;
        var content = responseBodyFactory is not null
            ? new ByteArrayContent(Encoding.UTF8.GetBytes(responseBodyFactory(request)))
            : ContentFactory?.Invoke() ??
              new ByteArrayContent(Encoding.UTF8.GetBytes("{\"ok\":true,\"data\":{\"status\":\"ok\"}}"));
        if (DeclaredLength.HasValue) content.Headers.ContentLength = DeclaredLength.Value;
        return Task.FromResult(new HttpResponseMessage(status) { Content = content });
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

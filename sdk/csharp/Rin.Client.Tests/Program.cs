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

sealed class RecordingHandler : HttpMessageHandler
{
    public HttpMethod? Method { get; private set; }
    public string Path { get; private set; } = string.Empty;
    public string Authorization { get; private set; } = string.Empty;
    public long? DeclaredLength { get; init; }
    public Func<HttpContent>? ContentFactory { get; init; }

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
        var content = ContentFactory?.Invoke() ??
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

using System.Diagnostics;
using System.Net;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Rin.Client;

public sealed class RinClient : IDisposable
{
    public const string ClientVersion = "0.6.0";
    public const string ProtocolVersion = "rin.protocol/v1";
    public const string DefaultBaseUrl = "http://127.0.0.1:7374";

    private const int MaxGenerationContentBytes = 4 * 1024 * 1024;
    private const decimal MaxJsonSafeInteger = 9_007_199_254_740_991m;
    private const int MaxJsonDepth = 64;

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    private readonly HttpClient httpClient;
    private readonly string baseUrl;
    private readonly string token;
    private readonly TimeSpan timeout;
    private readonly int maxResponseBytes;

    public RinClient(RinClientOptions? options = null)
        : this(options, CreateHandler())
    {
    }

    internal RinClient(RinClientOptions? options, HttpMessageHandler handler)
    {
        options ??= new RinClientOptions();
        token = ValidateToken(options.Token);
        baseUrl = NormalizeBaseUrl(options.BaseUrl, token);
        timeout = options.Timeout;
        if (timeout < TimeSpan.FromMilliseconds(50) || timeout > TimeSpan.FromSeconds(120))
        {
            throw new RinConfigurationException("invalid_timeout", "Timeout must be between 50 ms and 120 seconds");
        }
        maxResponseBytes = options.MaxResponseBytes;
        if (maxResponseBytes < 1024 || maxResponseBytes > 32 * 1024 * 1024)
        {
            throw new RinConfigurationException("invalid_response_limit", "Response limit must be between 1 KiB and 32 MiB");
        }

        httpClient = new HttpClient(handler ?? throw new ArgumentNullException(nameof(handler)), disposeHandler: true)
        {
            Timeout = Timeout.InfiniteTimeSpan,
        };
        httpClient.DefaultRequestHeaders.Accept.Add(new MediaTypeWithQualityHeaderValue("application/json"));
        httpClient.DefaultRequestHeaders.UserAgent.ParseAdd($"rin-csharp/{ClientVersion}");
    }

    public Task<JsonElement> HealthAsync(CancellationToken cancellationToken = default) =>
        RequestAsync(HttpMethod.Get, "/health", null, 200, cancellationToken);

    public Task<JsonElement> CreateSessionAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/create", payload, 200, cancellationToken);

    public Task<JsonElement> ObserveAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/observe", payload, 200, cancellationToken);

    public Task<JsonElement> ProposeAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/agent/propose", payload, 200, cancellationToken);

    public Task<JsonElement> SubmitProposalJobAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/jobs/propose", payload, 202, cancellationToken);

    public Task<JsonElement> GetProposalJobAsync(string jobId, CancellationToken cancellationToken = default) =>
        RequestAsync(HttpMethod.Get, "/v1/jobs/" + PathId(jobId), null, 200, cancellationToken);

    public Task<JsonElement> CancelProposalJobAsync(string jobId, CancellationToken cancellationToken = default) =>
        RequestAsync(HttpMethod.Delete, "/v1/jobs/" + PathId(jobId), null, 200, cancellationToken);

    public Task<JsonElement> SubmitGenerationJobAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/generation/jobs", payload, 202, cancellationToken);

    public Task<JsonElement> GetGenerationJobAsync(string jobId, CancellationToken cancellationToken = default) =>
        RequestAsync(HttpMethod.Get, "/v1/generation/jobs/" + PathId(jobId), null, 200, cancellationToken);

    public Task<JsonElement> CancelGenerationJobAsync(string jobId, CancellationToken cancellationToken = default) =>
        RequestAsync(HttpMethod.Delete, "/v1/generation/jobs/" + PathId(jobId), null, 200, cancellationToken);

    /// <summary>Reports an outcome the game already applied or rejected.</summary>
    public Task<JsonElement> CommitAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/action/commit", payload, 200, cancellationToken);

    /// <summary>Atomically reports outcomes produced from one original world revision.</summary>
    public Task<JsonElement> CommitBatchAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/action/commit-batch", payload, 200, cancellationToken);

    public Task<JsonElement> SetActorActivityAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/activity", payload, 200, cancellationToken);

    public Task<JsonElement> ArbitrateAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/world/arbitrate", payload, 200, cancellationToken);

    public Task<JsonElement> StateAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/get", payload, 200, cancellationToken);

    public Task<JsonElement> SnapshotAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/snapshot", payload, 200, cancellationToken);

    public Task<JsonElement> RestoreAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/restore", payload, 200, cancellationToken);

    public Task<JsonElement> TimelineAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/timeline", payload, 200, cancellationToken);

    public Task<JsonElement> ReplayAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/session/replay", payload, 200, cancellationToken);

    public Task<JsonElement> DueAgentsAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/scheduler/due", payload, 200, cancellationToken);

    public Task<JsonElement> WaitForProposalAsync(
        string jobId,
        TimeSpan? deadline = null,
        TimeSpan? interval = null,
        CancellationToken cancellationToken = default) =>
        WaitForJobAsync(
            jobId,
            GetProposalJobAsync,
            CancelProposalJobAsync,
            deadline ?? TimeSpan.FromSeconds(25),
            interval ?? TimeSpan.FromMilliseconds(100),
            JobResultKind.Proposal,
            cancellationToken);

    public Task<JsonElement> WaitForGenerationAsync(
        string jobId,
        TimeSpan? deadline = null,
        TimeSpan? interval = null,
        CancellationToken cancellationToken = default) =>
        WaitForJobAsync(
            jobId,
            GetGenerationJobAsync,
            CancelGenerationJobAsync,
            deadline ?? TimeSpan.FromSeconds(45),
            interval ?? TimeSpan.FromMilliseconds(100),
            JobResultKind.Generation,
            cancellationToken);

    public void Dispose() => httpClient.Dispose();

    private static HttpMessageHandler CreateHandler() => new HttpClientHandler
    {
        AllowAutoRedirect = false,
        AutomaticDecompression = DecompressionMethods.GZip | DecompressionMethods.Deflate,
    };

    private static async Task<JsonElement> WaitForJobAsync(
        string jobId,
        Func<string, CancellationToken, Task<JsonElement>> getter,
        Func<string, CancellationToken, Task<JsonElement>> canceler,
        TimeSpan deadline,
        TimeSpan interval,
        JobResultKind resultKind,
        CancellationToken cancellationToken)
    {
        if (deadline < TimeSpan.FromMilliseconds(50) || deadline > TimeSpan.FromMinutes(5) ||
            interval < TimeSpan.FromMilliseconds(10) || interval > TimeSpan.FromSeconds(5))
        {
            throw new RinConfigurationException("invalid_polling", "Job deadline or interval is out of range");
        }
        var elapsed = Stopwatch.StartNew();
        try
        {
            while (true)
            {
                var job = await getter(jobId, cancellationToken).ConfigureAwait(false);
                if (IsResolvedJob(job, resultKind, jobId)) return job;
                var remaining = deadline - elapsed.Elapsed;
                if (remaining <= TimeSpan.Zero)
                {
                    JsonElement canceledJob;
                    try
                    {
                        canceledJob = await canceler(jobId, CancellationToken.None).ConfigureAwait(false);
                    }
                    catch (RinException)
                    {
                        throw new RinApiException("job_timeout", "Rin job exceeded its deadline");
                    }
                    if (IsResolvedJob(canceledJob, resultKind, jobId)) return canceledJob;
                    throw new RinApiException("job_timeout", "Rin job exceeded its deadline");
                }
                await Task.Delay(interval < remaining ? interval : remaining, cancellationToken).ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException callerCancellation) when (cancellationToken.IsCancellationRequested)
        {
            return await ReconcileCallerCancellationAsync(
                jobId,
                canceler,
                resultKind,
                callerCancellation).ConfigureAwait(false);
        }
    }

    private static async Task<JsonElement> ReconcileCallerCancellationAsync(
        string jobId,
        Func<string, CancellationToken, Task<JsonElement>> canceler,
        JobResultKind resultKind,
        OperationCanceledException callerCancellation)
    {
        JsonElement canceledJob;
        try
        {
            // The caller token is already canceled. Reconciliation must get its own
            // request deadline so a raced, durable proposal cannot be discarded.
            canceledJob = await canceler(jobId, CancellationToken.None).ConfigureAwait(false);
        }
        catch (RinException)
        {
            throw new RinApiException(
                "job_cancel_unconfirmed",
                "Caller cancellation could not be confirmed with Rin");
        }
        catch (OperationCanceledException)
        {
            throw new RinApiException(
                "job_cancel_unconfirmed",
                "Caller cancellation could not be confirmed with Rin");
        }

        try
        {
            ValidateJobIdentity(canceledJob, resultKind, jobId, out _, out _);
            var status = RequiredRawJobStatus(canceledJob);
            if (status == "canceled")
            {
                throw callerCancellation;
            }
            if (IsResolvedJob(canceledJob, resultKind, jobId)) return canceledJob;
        }
        catch (RinProtocolException)
        {
            throw new RinApiException(
                "job_outcome_unknown",
                "Rin returned an invalid cancellation outcome");
        }

        throw new RinApiException(
            "job_outcome_unknown",
            "Rin did not confirm a terminal job outcome after caller cancellation");
    }

    private static bool IsResolvedJob(
        JsonElement job,
        JobResultKind resultKind,
        string expectedJobId)
    {
        ValidateJobIdentity(job, resultKind, expectedJobId, out var jobSessionId, out var jobRequestId);
        var status = RequiredRawJobStatus(job);
        if (status == "succeeded")
        {
            if (resultKind == JobResultKind.Proposal)
            {
                if (!job.TryGetProperty("proposal", out var proposal) || proposal.ValueKind != JsonValueKind.Object)
                {
                    throw new RinProtocolException("invalid_job", "Successful proposal job did not include a proposal");
                }
                if (!TryIdentifierProperty(proposal, "id", out _) ||
                    !TryIdentifierProperty(proposal, "actor_id", out _) ||
                    !TryIdentifierProperty(proposal, "session_id", out var proposalSessionId) ||
                    !TryIdentifierProperty(proposal, "request_id", out var proposalRequestId) ||
                    proposalSessionId != jobSessionId ||
                    proposalRequestId != jobRequestId ||
                    !TryNonnegativeJsonSafeIntegerProperty(proposal, "tick"))
                {
                    throw new RinProtocolException(
                        "invalid_job",
                        "Successful proposal job contained invalid identity fields");
                }
            }
            if (resultKind == JobResultKind.Generation)
            {
                if (!job.TryGetProperty("result", out var result) || result.ValueKind != JsonValueKind.Object ||
                    !result.TryGetProperty("content", out var content) || content.ValueKind != JsonValueKind.String)
                {
                    throw new RinProtocolException("invalid_job", "Successful generation job did not include content");
                }
                var value = content.GetString();
                if (string.IsNullOrWhiteSpace(value) ||
                    value.Contains('\0') ||
                    Encoding.UTF8.GetByteCount(value) > MaxGenerationContentBytes)
                {
                    throw new RinProtocolException("invalid_job", "Successful generation job did not include bounded content");
                }
            }
            return true;
        }
        if (status is "failed" or "stale" or "canceled")
        {
            var detail = job.TryGetProperty("error", out var error) && error.ValueKind == JsonValueKind.Object
                ? error
                : default;
            throw new RinApiException(
                TextProperty(detail, "code", 96, "job_" + status),
                TextProperty(detail, "message", 500, "Rin job ended as " + status));
        }
        if (status is not ("queued" or "running"))
        {
            throw new RinProtocolException("invalid_job", "Rin returned an unknown job status");
        }
        return false;
    }

    private static string RequiredRawJobStatus(JsonElement job)
    {
        if (job.ValueKind != JsonValueKind.Object ||
            !job.TryGetProperty("status", out var property) ||
            property.ValueKind != JsonValueKind.String)
        {
            throw new RinProtocolException("invalid_job", "Rin job status must be a string");
        }
        var status = property.GetString();
        if (status is not ("queued" or "running" or "succeeded" or "failed" or "stale" or "canceled"))
        {
            throw new RinProtocolException("invalid_job", "Rin returned an unknown job status");
        }
        return status;
    }

    private static void ValidateJobIdentity(
        JsonElement job,
        JobResultKind resultKind,
        string expectedJobId,
        out string sessionId,
        out string requestId)
    {
        sessionId = string.Empty;
        requestId = string.Empty;
        if (job.ValueKind != JsonValueKind.Object ||
            !TryIdentifierProperty(job, "job_id", out var responseJobId) ||
            responseJobId != expectedJobId)
        {
            throw new RinProtocolException(
                "invalid_job",
                "Rin returned a job with an invalid or mismatched job_id");
        }
        if (resultKind == JobResultKind.Proposal &&
            (!TryIdentifierProperty(job, "session_id", out sessionId) ||
             !TryIdentifierProperty(job, "request_id", out requestId)))
        {
            throw new RinProtocolException("invalid_job", "Rin returned a proposal job with invalid identity fields");
        }
        if (resultKind == JobResultKind.Generation &&
            !TryIdentifierProperty(job, "request_id", out requestId))
        {
            throw new RinProtocolException("invalid_job", "Rin returned a generation job with an invalid request_id");
        }
    }

    private static bool TryIdentifierProperty(JsonElement element, string name, out string value)
    {
        value = string.Empty;
        if (element.ValueKind != JsonValueKind.Object ||
            !element.TryGetProperty(name, out var property) ||
            property.ValueKind != JsonValueKind.String)
        {
            return false;
        }
        value = property.GetString() ?? string.Empty;
        return IsProtocolIdentifier(value);
    }

    private static bool IsProtocolIdentifier(string value)
    {
        if (value.Length is < 1 or > 96 || !IsAsciiLetterOrDigit(value[0])) return false;
        return value.All(character => IsAsciiLetterOrDigit(character) || character is '.' or '_' or '-');
    }

    private static bool IsAsciiLetterOrDigit(char value) =>
        value is >= 'a' and <= 'z' or >= 'A' and <= 'Z' or >= '0' and <= '9';

    private static bool TryNonnegativeJsonSafeIntegerProperty(JsonElement element, string name)
    {
        if (!element.TryGetProperty(name, out var property) ||
            property.ValueKind != JsonValueKind.Number ||
            !property.TryGetInt64(out var value) ||
            value < 0 ||
            value > (long)MaxJsonSafeInteger)
        {
            return false;
        }
        var token = property.GetRawText();
        return token.Length > 0 && token.All(character => character is >= '0' and <= '9');
    }

    private static void ValidateRequestJson(JsonElement value, int depth)
    {
        if (depth > MaxJsonDepth)
        {
            throw new RinProtocolException("invalid_request", "Rin payload exceeds the JSON nesting limit");
        }
        switch (value.ValueKind)
        {
            case JsonValueKind.Object:
                foreach (var property in value.EnumerateObject())
                {
                    ValidateRequestJson(property.Value, depth + 1);
                }
                return;
            case JsonValueKind.Array:
                foreach (var item in value.EnumerateArray())
                {
                    ValidateRequestJson(item, depth + 1);
                }
                return;
            case JsonValueKind.Number:
                if (value.TryGetDecimal(out var decimalValue))
                {
                    if (decimal.Truncate(decimalValue) == decimalValue &&
                        (decimalValue < -MaxJsonSafeInteger || decimalValue > MaxJsonSafeInteger))
                    {
                        throw new RinProtocolException("invalid_request", "Rin payload contains an unsafe JSON integer");
                    }
                    return;
                }
                if (!value.TryGetDouble(out var doubleValue) ||
                    !double.IsFinite(doubleValue))
                {
                    throw new RinProtocolException("invalid_request", "Rin payload contains a non-finite JSON number");
                }
                if (Math.Truncate(doubleValue) == doubleValue &&
                    (doubleValue < -(double)MaxJsonSafeInteger || doubleValue > (double)MaxJsonSafeInteger))
                {
                    throw new RinProtocolException("invalid_request", "Rin payload contains an unsafe JSON integer");
                }
                return;
            case JsonValueKind.String:
            case JsonValueKind.True:
            case JsonValueKind.False:
            case JsonValueKind.Null:
                return;
            default:
                throw new RinProtocolException("invalid_request", "Rin payload contains a non-JSON value");
        }
    }

    private enum JobResultKind
    {
        Proposal,
        Generation,
    }

    private Task<JsonElement> PostAsync(string path, object payload, int expectedStatus, CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(payload);
        return RequestAsync(HttpMethod.Post, path, payload, expectedStatus, cancellationToken);
    }

    private async Task<JsonElement> RequestAsync(
        HttpMethod method,
        string path,
        object? payload,
        int expectedStatus,
        CancellationToken cancellationToken)
    {
        if (!path.StartsWith("/", StringComparison.Ordinal) || path.Contains("//", StringComparison.Ordinal) || path.Contains("..", StringComparison.Ordinal))
        {
            throw new RinConfigurationException("invalid_path", "Rin request path is invalid");
        }

        using var request = new HttpRequestMessage(method, baseUrl + path);
        if (payload is not null)
        {
            byte[] encoded;
            try
            {
                encoded = JsonSerializer.SerializeToUtf8Bytes(payload, payload.GetType(), JsonOptions);
                using var document = JsonDocument.Parse(encoded);
                if (document.RootElement.ValueKind != JsonValueKind.Object)
                {
                    throw new RinProtocolException("invalid_request", "Rin payload must be an object");
                }
                ValidateRequestJson(document.RootElement, 0);
            }
            catch (Exception exception) when (exception is JsonException or NotSupportedException or ArgumentException)
            {
                throw new RinProtocolException("invalid_request", "Rin payload is not JSON serializable", exception);
            }
            request.Content = new ByteArrayContent(encoded);
            request.Content.Headers.ContentType = new MediaTypeHeaderValue("application/json") { CharSet = "utf-8" };
        }
        if (token.Length > 0)
        {
            request.Headers.TryAddWithoutValidation("Authorization", "Bearer " + token);
        }

        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        deadline.CancelAfter(timeout);
        HttpResponseMessage response;
        try
        {
            response = await httpClient.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, deadline.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException exception) when (!cancellationToken.IsCancellationRequested)
        {
            throw new RinTransportException("transport_timeout", "Rin request timed out", exception);
        }
        catch (HttpRequestException exception)
        {
            throw new RinTransportException("transport_failed", "Rin is unavailable", exception);
        }

        using (response)
        {
            if (response.Headers.Location is not null && (int)response.StatusCode is >= 300 and < 400)
            {
                throw new RinTransportException("redirect_rejected", "Rin endpoint attempted to redirect");
            }
            if (response.Content.Headers.ContentLength is long declared && declared > maxResponseBytes)
            {
                throw new RinProtocolException("response_too_large", "Rin response exceeds the configured limit");
            }

            byte[] raw;
            try
            {
                raw = await ReadBoundedAsync(response.Content, deadline.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException exception) when (!cancellationToken.IsCancellationRequested)
            {
                throw new RinTransportException("transport_timeout", "Rin request timed out", exception);
            }
            catch (Exception exception) when (exception is HttpRequestException or IOException)
            {
                throw new RinTransportException("transport_failed", "Rin response could not be read", exception);
            }
            JsonDocument document;
            try
            {
                document = JsonDocument.Parse(raw);
            }
            catch (JsonException exception)
            {
                if ((int)response.StatusCode != expectedStatus)
                {
                    throw new RinApiException("http_error", "Rin request failed", (int)response.StatusCode);
                }
                throw new RinProtocolException("invalid_response", "Rin returned invalid JSON", exception);
            }

            using (document)
            {
                var root = document.RootElement;
                if (root.ValueKind != JsonValueKind.Object)
                {
                    throw new RinProtocolException("invalid_response", "Rin response must be an object");
                }
                var ok = root.TryGetProperty("ok", out var okElement) && okElement.ValueKind == JsonValueKind.True;
                if ((int)response.StatusCode != expectedStatus || !ok)
                {
                    throw ApiError(root, (int)response.StatusCode);
                }
                if (!root.TryGetProperty("data", out var data) || data.ValueKind != JsonValueKind.Object)
                {
                    throw new RinProtocolException("invalid_response", "Rin response data must be an object");
                }
                return data.Clone();
            }
        }
    }

    private async Task<byte[]> ReadBoundedAsync(HttpContent content, CancellationToken cancellationToken)
    {
        await using var stream = await content.ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
        using var output = new MemoryStream();
        var buffer = new byte[8192];
        while (true)
        {
            var count = await stream.ReadAsync(buffer.AsMemory(0, buffer.Length), cancellationToken).ConfigureAwait(false);
            if (count == 0) break;
            if (output.Length + count > maxResponseBytes)
            {
                throw new RinProtocolException("response_too_large", "Rin response exceeds the configured limit");
            }
            output.Write(buffer, 0, count);
        }
        return output.ToArray();
    }

    private static RinApiException ApiError(JsonElement root, int status)
    {
        var detail = root.TryGetProperty("error", out var error) && error.ValueKind == JsonValueKind.Object
            ? error
            : default;
        return new RinApiException(
            TextProperty(detail, "code", 96, "http_error"),
            TextProperty(detail, "message", 500, "Rin request failed"),
            status,
            TextProperty(detail, "field", 160));
    }

    private static string TextProperty(JsonElement element, string name, int maximum, string fallback = "")
    {
        if (element.ValueKind == JsonValueKind.Object && element.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.String)
        {
            return RinException.SafeText(value.GetString(), maximum, fallback);
        }
        return fallback;
    }

    private static string NormalizeBaseUrl(string? value, string validatedToken)
    {
        if (!Uri.TryCreate((value ?? DefaultBaseUrl).Trim().TrimEnd('/'), UriKind.Absolute, out var uri) ||
            (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps) ||
            uri.Host.Length == 0 || uri.UserInfo.Length > 0 || uri.Query.Length > 0 || uri.Fragment.Length > 0 ||
            (uri.AbsolutePath.Length > 0 && uri.AbsolutePath != "/"))
        {
            throw new RinConfigurationException("invalid_base_url", "Rin base URL must be an origin");
        }
        var loopback = uri.Host.Equals("localhost", StringComparison.OrdinalIgnoreCase) ||
            (IPAddress.TryParse(uri.Host, out var address) && IPAddress.IsLoopback(address));
        if (uri.Scheme == Uri.UriSchemeHttp && !loopback)
        {
            throw new RinConfigurationException("insecure_base_url", "Remote Rin endpoints must use HTTPS");
        }
        if (!loopback && validatedToken.Length == 0)
        {
            throw new RinConfigurationException("missing_token", "Remote Rin endpoints require a token");
        }
        return uri.GetLeftPart(UriPartial.Authority);
    }

    private static string ValidateToken(string? value)
    {
        var candidate = value ?? string.Empty;
        if (candidate.Length > 4096 || candidate != candidate.Trim() || candidate.IndexOfAny(new[] { '\0', '\r', '\n' }) >= 0)
        {
            throw new RinConfigurationException("invalid_token", "Rin token must be a bounded single-line value");
        }
        return candidate;
    }

    private static string PathId(string? value)
    {
        if (string.IsNullOrEmpty(value) || value.Length > 96 || value.Any(character =>
                !((character >= 'a' && character <= 'z') ||
                  (character >= 'A' && character <= 'Z') ||
                  (character >= '0' && character <= '9') ||
                  character is '.' or '_' or '-')))
        {
            throw new RinConfigurationException("invalid_identifier", "Rin path identifier is invalid");
        }
        return Uri.EscapeDataString(value);
    }
}

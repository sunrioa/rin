using System.Diagnostics;
using System.Net;
using System.Net.Http.Headers;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Rin.Client;

public sealed class RinClient : IDisposable
{
    public const string ProtocolVersion = "rin.protocol/v1";
    public const string DefaultBaseUrl = "http://127.0.0.1:7374";

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
        httpClient.DefaultRequestHeaders.UserAgent.ParseAdd("rin-csharp/0.5");
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

    public Task<JsonElement> CommitAsync(object payload, CancellationToken cancellationToken = default) =>
        PostAsync("/v1/action/commit", payload, 200, cancellationToken);

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
        CancellationToken cancellationToken)
    {
        if (deadline < TimeSpan.FromMilliseconds(50) || deadline > TimeSpan.FromMinutes(5) ||
            interval < TimeSpan.FromMilliseconds(10) || interval > TimeSpan.FromSeconds(5))
        {
            throw new RinConfigurationException("invalid_polling", "Job deadline or interval is out of range");
        }
        var elapsed = Stopwatch.StartNew();
        while (true)
        {
            var job = await getter(jobId, cancellationToken).ConfigureAwait(false);
            var status = TextProperty(job, "status", 32);
            if (status == "succeeded") return job;
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
            var remaining = deadline - elapsed.Elapsed;
            if (remaining <= TimeSpan.Zero)
            {
                try { await canceler(jobId, CancellationToken.None).ConfigureAwait(false); }
                catch (RinException) { }
                throw new RinApiException("job_timeout", "Rin job exceeded its deadline");
            }
            await Task.Delay(interval < remaining ? interval : remaining, cancellationToken).ConfigureAwait(false);
        }
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
            }
            catch (Exception exception) when (exception is JsonException or NotSupportedException)
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

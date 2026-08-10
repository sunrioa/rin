using System.Net;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;

namespace Rin.Client;

/// <summary>Thin loopback client for the engine-neutral Control V2 contract.</summary>
public sealed class RinControlClient : IDisposable
{
    public const string ContractVersion = "rin.control/v2";
    public const string DefaultBaseUrl = "http://127.0.0.1:7375";
    public const int MaximumResponseBytes = 8 * 1024 * 1024;

    private const decimal MaxJsonSafeInteger = 9_007_199_254_740_991m;
    private const int MaxJsonDepth = 64;

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
    };

    private readonly HttpClient httpClient;
    private readonly string baseUrl;
    private readonly string token;
    private readonly TimeSpan timeout;
    private readonly int maxResponseBytes;

    public RinControlClient(RinControlClientOptions options)
        : this(options, CreateHandler())
    {
    }

    internal RinControlClient(
        RinControlClientOptions options,
        HttpMessageHandler handler)
    {
        if (options is null) throw new ArgumentNullException(nameof(options));
        token = ValidateToken(options.Token);
        baseUrl = NormalizeBaseUrl(options.BaseUrl);
        timeout = options.Timeout;
        if (timeout < TimeSpan.FromMilliseconds(50) ||
            timeout > TimeSpan.FromSeconds(120))
        {
            throw new RinConfigurationException(
                "invalid_timeout",
                "Control timeout must be between 50 ms and 120 seconds");
        }
        maxResponseBytes = options.MaxResponseBytes;
        if (maxResponseBytes < 1024 || maxResponseBytes > MaximumResponseBytes)
        {
            throw new RinConfigurationException(
                "invalid_response_limit",
                "Control response limit must be between 1 KiB and 8 MiB");
        }
        httpClient = new HttpClient(
            handler ?? throw new ArgumentNullException(nameof(handler)),
            disposeHandler: true)
        {
            Timeout = Timeout.InfiniteTimeSpan,
        };
        httpClient.DefaultRequestHeaders.Accept.Add(
            new MediaTypeWithQualityHeaderValue("application/json"));
        httpClient.DefaultRequestHeaders.UserAgent.ParseAdd(
            $"rin-control-csharp/{RinClient.ClientVersion}");
    }

    public async Task<JsonElement> InfoAsync(
        CancellationToken cancellationToken = default)
    {
        var info = await RequestAsync(
            HttpMethod.Get,
            "/control/v2/info",
            null,
            cancellationToken).ConfigureAwait(false);
        if (info.ValueKind != JsonValueKind.Object ||
            !info.TryGetProperty("contract_version", out var contract) ||
            contract.ValueKind != JsonValueKind.String ||
            contract.GetString() != ContractVersion)
        {
            throw new RinProtocolException(
                "control_contract_mismatch",
                "Control Daemon returned an unsupported contract");
        }
        return info;
    }

    public Task<JsonElement> ListWorldsAsync(
        CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/worlds", new Dictionary<string, object>(), cancellationToken);

    public Task<JsonElement> ListActorsAsync(
        object input,
        CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/actors", input, cancellationToken);

    public Task<JsonElement> GetActorAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/actor", input, cancellationToken);

    public Task<JsonElement> WaitActorAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/wait-actor", input, cancellationToken);

    public Task<JsonElement> ObserveActorAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/observe", input, cancellationToken);

    public Task<JsonElement> ListCapabilitiesAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/capabilities", input, cancellationToken);

    public Task<JsonElement> DescribeCapabilityAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/capability", input, cancellationToken);

    public Task<JsonElement> AcquireControllerAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/controllers/acquire", input, cancellationToken);

    public Task<JsonElement> RenewControllerAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/controllers/renew", input, cancellationToken);

    public Task<JsonElement> ReleaseControllerAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/controllers/release", input, cancellationToken);

    public Task<JsonElement> GetControllerAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/controllers/get", input, cancellationToken);

    public Task<JsonElement> SubmitActionAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/actions/submit", input, cancellationToken);

    public Task<JsonElement> ConfirmActionAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/actions/confirm", input, cancellationToken);

    public Task<JsonElement> GetOperationAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/operations/get", input, cancellationToken);

    public Task<JsonElement> WaitOperationAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/operations/wait", input, cancellationToken);

    public Task<JsonElement> CancelOperationAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/operations/cancel", input, cancellationToken);

    public Task<JsonElement> SetEmergencyStopAsync(object input, CancellationToken cancellationToken = default) =>
        PostAsync("/control/v2/emergency-stop", input, cancellationToken);

    public void Dispose() => httpClient.Dispose();

    private static HttpMessageHandler CreateHandler() => new HttpClientHandler
    {
        AllowAutoRedirect = false,
        AutomaticDecompression = DecompressionMethods.GZip | DecompressionMethods.Deflate,
    };

    private Task<JsonElement> PostAsync(
        string path,
        object input,
        CancellationToken cancellationToken)
    {
        if (input is null) throw new ArgumentNullException(nameof(input));
        return RequestAsync(HttpMethod.Post, path, input, cancellationToken);
    }

    private async Task<JsonElement> RequestAsync(
        HttpMethod method,
        string path,
        object? input,
        CancellationToken cancellationToken)
    {
        using var request = new HttpRequestMessage(method, baseUrl + path);
        request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);
        if (input is not null) request.Content = JsonContent(input);

        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        deadline.CancelAfter(timeout);
        HttpResponseMessage response;
        try
        {
            response = await httpClient.SendAsync(
                request,
                HttpCompletionOption.ResponseHeadersRead,
                deadline.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException exception)
            when (!cancellationToken.IsCancellationRequested)
        {
            throw new RinTransportException(
                "transport_timeout",
                "Control Daemon request timed out",
                exception);
        }
        catch (HttpRequestException exception)
        {
            throw new RinTransportException(
                "transport_failed",
                "Control Daemon is unavailable",
                exception);
        }

        using (response)
        {
            var status = (int)response.StatusCode;
            if (status is >= 300 and < 400)
            {
                throw new RinTransportException(
                    "redirect_rejected",
                    "Control Daemon attempted to redirect");
            }
            if (!string.Equals(
                response.Content.Headers.ContentType?.MediaType,
                "application/json",
                StringComparison.OrdinalIgnoreCase))
            {
                throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon response must be application/json");
            }
            if (response.Content.Headers.ContentLength is long declared &&
                (declared < 0 || declared > maxResponseBytes))
            {
                throw new RinProtocolException(
                    "response_too_large",
                    "Control Daemon response exceeds the configured limit");
            }

            byte[] raw;
            try
            {
                raw = await ReadBoundedAsync(
                    response.Content,
                    deadline.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException exception)
                when (!cancellationToken.IsCancellationRequested)
            {
                throw new RinTransportException(
                    "transport_timeout",
                    "Control Daemon request timed out",
                    exception);
            }
            catch (Exception exception)
                when (exception is HttpRequestException or IOException)
            {
                throw new RinTransportException(
                    "transport_failed",
                    "Control Daemon response could not be read",
                    exception);
            }
            return DecodeResponse(raw, status);
        }
    }

    private ByteArrayContent JsonContent(object input)
    {
        byte[] encoded;
        try
        {
            encoded = JsonSerializer.SerializeToUtf8Bytes(
                input,
                input.GetType(),
                JsonOptions);
            using var document = JsonDocument.Parse(encoded);
            if (document.RootElement.ValueKind != JsonValueKind.Object)
            {
                throw new RinProtocolException(
                    "invalid_request",
                    "Control payload must be an object");
            }
            ValidateRequestJson(document.RootElement, 0);
        }
        catch (Exception exception)
            when (exception is JsonException or NotSupportedException or ArgumentException)
        {
            throw new RinProtocolException(
                "invalid_request",
                "Control payload is not JSON serializable",
                exception);
        }
        var content = new ByteArrayContent(encoded);
        content.Headers.ContentType =
            new MediaTypeHeaderValue("application/json") { CharSet = "utf-8" };
        return content;
    }

    private async Task<byte[]> ReadBoundedAsync(
        HttpContent content,
        CancellationToken cancellationToken)
    {
#if NETSTANDARD2_0
        using var stream = await content.ReadAsStreamAsync().ConfigureAwait(false);
#else
        await using var stream = await content
            .ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
#endif
        using var output = new MemoryStream();
        var buffer = new byte[8192];
        while (true)
        {
#if NETSTANDARD2_0
            var count = await stream.ReadAsync(
                buffer, 0, buffer.Length, cancellationToken).ConfigureAwait(false);
#else
            var count = await stream.ReadAsync(
                buffer.AsMemory(0, buffer.Length), cancellationToken).ConfigureAwait(false);
#endif
            if (count == 0) break;
            if (output.Length + count > maxResponseBytes)
            {
                throw new RinProtocolException(
                    "response_too_large",
                    "Control Daemon response exceeds the configured limit");
            }
            output.Write(buffer, 0, count);
        }
        return output.ToArray();
    }

    private static JsonElement DecodeResponse(byte[] raw, int status)
    {
        JsonDocument document;
        try
        {
            document = JsonDocument.Parse(raw);
        }
        catch (JsonException exception)
        {
            if (status < 200 || status >= 300)
            {
                throw new RinApiException(
                    ErrorCode(status),
                    "Control Daemon request failed",
                    status);
            }
            throw new RinProtocolException(
                "invalid_response",
                "Control Daemon returned invalid JSON",
                exception);
        }
        using (document)
        {
            var root = document.RootElement;
            if (root.ValueKind is not (JsonValueKind.Object or JsonValueKind.Array))
            {
                throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon response must be an object or array");
            }
            if (status < 200 || status >= 300)
            {
                var code = root.ValueKind == JsonValueKind.Object &&
                    root.TryGetProperty("code", out var codeValue) &&
                    codeValue.ValueKind == JsonValueKind.String
                        ? RinException.SafeText(codeValue.GetString(), 96, ErrorCode(status))
                        : ErrorCode(status);
                var message = root.ValueKind == JsonValueKind.Object &&
                    root.TryGetProperty("error", out var errorValue) &&
                    errorValue.ValueKind == JsonValueKind.String
                        ? RinException.SafeText(
                            errorValue.GetString(),
                            500,
                            "Control Daemon request failed")
                        : "Control Daemon request failed";
                throw new RinApiException(code, message, status);
            }
            return root.Clone();
        }
    }

    private static void ValidateRequestJson(JsonElement value, int depth)
    {
        if (depth > MaxJsonDepth)
        {
            throw new RinProtocolException(
                "invalid_request",
                "Control payload exceeds the JSON nesting limit");
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
                        (decimalValue < -MaxJsonSafeInteger ||
                         decimalValue > MaxJsonSafeInteger))
                    {
                        throw new RinProtocolException(
                            "invalid_request",
                            "Control payload contains an unsafe JSON integer");
                    }
                    return;
                }
                if (!value.TryGetDouble(out var doubleValue) ||
                    double.IsNaN(doubleValue) ||
                    double.IsInfinity(doubleValue))
                {
                    throw new RinProtocolException(
                        "invalid_request",
                        "Control payload contains a non-finite JSON number");
                }
                if (Math.Truncate(doubleValue) == doubleValue &&
                    (doubleValue < -(double)MaxJsonSafeInteger ||
                     doubleValue > (double)MaxJsonSafeInteger))
                {
                    throw new RinProtocolException(
                        "invalid_request",
                        "Control payload contains an unsafe JSON integer");
                }
                return;
            case JsonValueKind.String:
            case JsonValueKind.True:
            case JsonValueKind.False:
            case JsonValueKind.Null:
                return;
            default:
                throw new RinProtocolException(
                    "invalid_request",
                    "Control payload contains a non-JSON value");
        }
    }

    private static string NormalizeBaseUrl(string? value)
    {
        var raw = (value ?? DefaultBaseUrl).Trim().TrimEnd('/');
        if (!Uri.TryCreate(raw, UriKind.Absolute, out var uri) ||
            uri.Scheme != Uri.UriSchemeHttp ||
            uri.Host.Length == 0 ||
            uri.UserInfo.Length > 0 ||
            uri.Query.Length > 0 ||
            uri.Fragment.Length > 0 ||
            (uri.AbsolutePath.Length > 0 && uri.AbsolutePath != "/") ||
            !HasExplicitPort(uri) ||
            !IsLoopback(uri.Host))
        {
            throw new RinConfigurationException(
                "invalid_base_url",
                "Control Daemon URL must be a plain loopback HTTP origin with an explicit port");
        }
        return uri.GetLeftPart(UriPartial.Authority);
    }

    private static bool HasExplicitPort(Uri uri)
    {
        var authority = uri.Authority;
        return authority.StartsWith("[", StringComparison.Ordinal)
            ? authority.IndexOf("]:", StringComparison.Ordinal) >= 0
            : authority.LastIndexOf(':') >= 0;
    }

    private static bool IsLoopback(string host) =>
        host.Equals("localhost", StringComparison.OrdinalIgnoreCase) ||
        (IPAddress.TryParse(host, out var address) && IPAddress.IsLoopback(address));

    private static string ValidateToken(string? value)
    {
        var candidate = value ?? string.Empty;
        if (candidate.Length > 4096 ||
            candidate != candidate.Trim() ||
            candidate.IndexOfAny(new[] { '\0', '\r', '\n' }) >= 0 ||
            Encoding.UTF8.GetByteCount(candidate) < 32)
        {
            throw new RinConfigurationException(
                "invalid_token",
                "Control token must be a bounded single-line value containing at least 32 bytes");
        }
        return candidate;
    }

    private static string ErrorCode(int status) => status switch
    {
        400 => "invalid",
        401 or 403 => "forbidden",
        404 => "not_found",
        409 => "conflict",
        410 => "unavailable",
        429 => "capacity",
        _ => "unavailable",
    };
}

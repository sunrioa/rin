namespace Rin.Client;

public sealed class RinClientOptions
{
    public string BaseUrl { get; init; } = RinClient.DefaultBaseUrl;

    public string Token { get; init; } = string.Empty;

    public TimeSpan Timeout { get; init; } = TimeSpan.FromSeconds(5);

    public int MaxResponseBytes { get; init; } = 32 * 1024 * 1024;
}

namespace Rin.Client;

public sealed class RinControlClientOptions
{
    public string BaseUrl { get; init; } = RinControlClient.DefaultBaseUrl;

    public string Token { get; init; } = string.Empty;

    public TimeSpan Timeout { get; init; } = TimeSpan.FromSeconds(30);

    public int MaxResponseBytes { get; init; } = RinControlClient.MaximumResponseBytes;
}

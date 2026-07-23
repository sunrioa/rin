namespace Rin.Client;

public class RinException : Exception
{
    public RinException(string code, string message, Exception? innerException = null)
        : base(SafeText(message, 500, "Rin request failed"), innerException)
    {
        Code = SafeText(code, 96, "rin_error");
    }

    public string Code { get; }

    internal static string SafeText(string? value, int maximum, string fallback = "")
    {
        var cleaned = string.Join(" ", (value ?? string.Empty)
            .Replace("\0", string.Empty, StringComparison.Ordinal)
            .Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries));
        return cleaned.Length == 0 ? fallback : cleaned[..Math.Min(cleaned.Length, maximum)];
    }
}

public sealed class RinConfigurationException : RinException
{
    public RinConfigurationException(string code, string message, Exception? innerException = null)
        : base(code, message, innerException) { }
}

public sealed class RinTransportException : RinException
{
    public RinTransportException(string code, string message, Exception? innerException = null)
        : base(code, message, innerException) { }
}

public sealed class RinProtocolException : RinException
{
    public RinProtocolException(string code, string message, Exception? innerException = null)
        : base(code, message, innerException) { }
}

public sealed class RinApiException : RinException
{
    public RinApiException(string code, string message, int status = 0, string? field = null)
        : base(code, message)
    {
        Status = status;
        Field = SafeText(field, 160);
    }

    public int Status { get; }

    public string Field { get; }
}

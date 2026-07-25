namespace Rin.Client;

public sealed class RinBinding
{
    public RinBinding(
        string gameId,
        string contentId,
        string contentVersion,
        string contentHash)
    {
        GameId = ValidateIdentifier(gameId, nameof(gameId));
        ContentId = ValidateIdentifier(contentId, nameof(contentId));
        ContentVersion = ValidateText(contentVersion, 64, nameof(contentVersion));
        ContentHash = ValidateText(contentHash, 128, nameof(contentHash));
    }

    public string GameId { get; }

    public string ContentId { get; }

    public string ContentVersion { get; }

    public string ContentHash { get; }

    private static string ValidateIdentifier(string value, string name)
    {
        var result = ValidateText(value, 96, name);
        if (!IsAsciiLetterOrDigit(result[0]) ||
            result.Any(character =>
                !IsAsciiLetterOrDigit(character) &&
                character is not ('.' or '_' or '-')))
        {
            throw new RinConfigurationException(
                "invalid_binding",
                "Expected Binding " + name + " is invalid");
        }
        return result;
    }

    private static string ValidateText(string? value, int maximum, string name)
    {
        if (string.IsNullOrEmpty(value) ||
            value.Length > maximum ||
            value.IndexOfAny(new[] { '\0', '\r', '\n' }) >= 0)
        {
            throw new RinConfigurationException(
                "invalid_binding",
                "Expected Binding " + name + " is invalid");
        }
        return value;
    }

    private static bool IsAsciiLetterOrDigit(char value) =>
        value is >= 'a' and <= 'z' or >= 'A' and <= 'Z' or >= '0' and <= '9';
}

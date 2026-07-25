using System.Security.Cryptography;
using System.Text.RegularExpressions;

namespace Rin.Client;

public static class RinIds
{
    private static readonly Regex PrefixPattern = new(
        "^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$",
        RegexOptions.CultureInvariant);
    private static readonly Regex IdentifierPattern = new(
        "^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$",
        RegexOptions.CultureInvariant);

    public static string Create(string prefix = "id")
    {
        if (prefix is null || !PrefixPattern.IsMatch(prefix))
        {
            throw new RinConfigurationException(
                "invalid_id_prefix",
                "ID prefix must be a protocol identifier no longer than 63 characters");
        }

        var random = new byte[16];
        using (var generator = RandomNumberGenerator.Create())
        {
            generator.GetBytes(random);
        }
        return prefix + "." + BitConverter.ToString(random)
            .Replace("-", string.Empty)
            .ToLowerInvariant();
    }

    public static bool IsValid(string? value) =>
        value is not null &&
        IdentifierPattern.IsMatch(value);
}

using System.Security.Cryptography;
using System.Text.RegularExpressions;

namespace Rin.Client;

public static class RinIds
{
    private static readonly Regex PrefixPattern = new(
        "^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$",
        RegexOptions.CultureInvariant);

    public static string Create(string prefix = "id")
    {
        if (prefix is null || !PrefixPattern.IsMatch(prefix))
        {
            throw new RinConfigurationException(
                "invalid_id_prefix",
                "ID prefix must be a protocol identifier no longer than 63 characters");
        }

        Span<byte> random = stackalloc byte[16];
        RandomNumberGenerator.Fill(random);
        return prefix + "." + Convert.ToHexString(random).ToLowerInvariant();
    }
}

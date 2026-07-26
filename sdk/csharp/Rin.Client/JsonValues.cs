using System.Text.Json;

namespace Rin.Client;

internal static class JsonValues
{
    public static bool Equivalent(JsonElement left, JsonElement right)
    {
        if (left.ValueKind != right.ValueKind) return false;
        switch (left.ValueKind)
        {
            case JsonValueKind.Object:
                var rightProperties = new Dictionary<string, JsonElement>();
                foreach (var property in right.EnumerateObject())
                {
                    if (rightProperties.ContainsKey(property.Name)) return false;
                    rightProperties[property.Name] = property.Value;
                }
                var count = 0;
                var leftNames = new HashSet<string>();
                foreach (var property in left.EnumerateObject())
                {
                    if (!leftNames.Add(property.Name)) return false;
                    count++;
                    if (!rightProperties.TryGetValue(property.Name, out var value) ||
                        !Equivalent(property.Value, value))
                    {
                        return false;
                    }
                }
                return count == rightProperties.Count;
            case JsonValueKind.Array:
                var leftItems = left.EnumerateArray();
                var rightItems = right.EnumerateArray();
                while (leftItems.MoveNext())
                {
                    if (!rightItems.MoveNext() ||
                        !Equivalent(leftItems.Current, rightItems.Current))
                    {
                        return false;
                    }
                }
                return !rightItems.MoveNext();
            case JsonValueKind.Number:
                return left.TryGetDecimal(out var leftNumber) &&
                    right.TryGetDecimal(out var rightNumber)
                        ? leftNumber == rightNumber
                        : left.GetRawText() == right.GetRawText();
            case JsonValueKind.String:
                return left.GetString() == right.GetString();
            case JsonValueKind.True:
            case JsonValueKind.False:
                return left.GetBoolean() == right.GetBoolean();
            case JsonValueKind.Null:
                return true;
            case JsonValueKind.Undefined:
                return false;
            default:
                return false;
        }
    }
}

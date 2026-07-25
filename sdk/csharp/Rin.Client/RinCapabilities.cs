using System.Text.Json;

namespace Rin.Client;

public static class RinFeatures
{
    public const string MemoryArchive = "memory-archive-v1";
    public const string BeliefConflicts = "belief-conflicts-v1";
    public const string GoalCandidates = "goal-candidates-v1";
    public const string ActorActivity = "actor-activity-v1";
    public const string Arbitration = "arbitration-v1";
    public const string OutcomeReporting = "outcome-reporting-v1";

    public static IReadOnlyList<string> AuthoritativePreset { get; } =
        Array.AsReadOnly(new[] { OutcomeReporting });

    public static IReadOnlyList<string> FullPreset { get; } = Array.AsReadOnly(new[]
    {
        MemoryArchive,
        BeliefConflicts,
        GoalCandidates,
        ActorActivity,
        Arbitration,
        OutcomeReporting,
    });
}

public sealed record RinCapabilities(
    string ProtocolVersion,
    string ReleaseVersion,
    string ReleaseStatus,
    string PolicyMode,
    bool AsyncJobs,
    bool StructuredGeneration,
    IReadOnlySet<string> Features)
{
    internal static RinCapabilities FromHealth(JsonElement health)
    {
        try
        {
            var protocolVersion = RequiredString(health, "protocol_version");
            if (!string.Equals(
                protocolVersion,
                RinClient.ProtocolVersion,
                StringComparison.Ordinal))
            {
                throw new RinProtocolException(
                    "protocol_mismatch",
                    $"Rin reports protocol {protocolVersion}");
            }

            var features = new HashSet<string>(StringComparer.Ordinal);
            foreach (var feature in health.GetProperty("features").EnumerateArray())
            {
                var value = feature.GetString();
                if (string.IsNullOrEmpty(value))
                {
                    throw new InvalidOperationException();
                }
                features.Add(value);
            }

            return new RinCapabilities(
                protocolVersion,
                RequiredString(health, "release_version"),
                RequiredString(health, "release_status"),
                RequiredString(health, "policy_mode"),
                health.GetProperty("async_jobs").GetBoolean(),
                health.GetProperty("structured_generation").GetBoolean(),
                features);
        }
        catch (RinProtocolException)
        {
            throw;
        }
        catch (Exception exception)
            when (exception is InvalidOperationException or KeyNotFoundException)
        {
            throw new RinProtocolException(
                "invalid_health",
                "Rin health response does not match the protocol",
                exception);
        }
    }

    private static string RequiredString(JsonElement value, string property)
    {
        var result = value.GetProperty(property).GetString();
        if (string.IsNullOrEmpty(result))
        {
            throw new InvalidOperationException();
        }
        return result;
    }
}

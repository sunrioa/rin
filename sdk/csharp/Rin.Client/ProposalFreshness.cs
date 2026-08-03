using System.Text.Json;

namespace Rin.Client;

public enum ProposalFreshnessDecision
{
    Fresh,
    Stale,
}

public static class ProposalFreshness
{
    private const long MaxJsonSafeInteger = 9_007_199_254_740_991L;

    public static ProposalFreshnessDecision Evaluate(
        JsonElement state,
        ActionProposal proposal)
    {
        Guard.NotNull(proposal, nameof(proposal));
        if (state.ValueKind != JsonValueKind.Object ||
            !state.TryGetProperty("proposals", out var proposals) ||
            proposals.ValueKind != JsonValueKind.Object ||
            !proposals.TryGetProperty(proposal.Id, out var retained) ||
            retained.ValueKind != JsonValueKind.Object ||
            !retained.TryGetProperty("status", out var status) ||
            status.ValueKind != JsonValueKind.String ||
            status.GetString() != "pending")
        {
            return ProposalFreshnessDecision.Stale;
        }
        if (proposal.BasedOnWorldRevision is not null)
        {
            var basedOnWorld = proposal.BasedOnWorldRevision.Value;
            return state.TryGetProperty("world_revision", out var world) &&
                TryPositiveSafeInteger(world, out var revision) &&
                PositiveSafeInteger(basedOnWorld) &&
                revision == basedOnWorld
                    ? ProposalFreshnessDecision.Fresh
                    : ProposalFreshnessDecision.Stale;
        }
        return state.TryGetProperty("revision", out var sessionRevision) &&
            TryPositiveSafeInteger(sessionRevision, out var current) &&
            PositiveSafeInteger(proposal.CreatedRevision) &&
            current == proposal.CreatedRevision
                ? ProposalFreshnessDecision.Fresh
                : ProposalFreshnessDecision.Stale;
    }

    private static bool TryPositiveSafeInteger(JsonElement value, out long result)
    {
        result = 0;
        return value.ValueKind == JsonValueKind.Number &&
            value.TryGetInt64(out result) &&
            PositiveSafeInteger(result);
    }

    private static bool PositiveSafeInteger(long value) =>
        value > 0 && value <= MaxJsonSafeInteger;
}

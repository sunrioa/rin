using System.Text.Json;

namespace Rin.Client;

public enum ProposalFreshnessDecision
{
    Fresh,
    Stale,
}

public static class ProposalFreshness
{
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
        if (proposal.BasedOnWorldRevision is > 0)
        {
            return state.TryGetProperty("world_revision", out var world) &&
                world.ValueKind == JsonValueKind.Number &&
                world.TryGetInt64(out var revision) &&
                revision == proposal.BasedOnWorldRevision
                    ? ProposalFreshnessDecision.Fresh
                    : ProposalFreshnessDecision.Stale;
        }
        return state.TryGetProperty("revision", out var sessionRevision) &&
            sessionRevision.ValueKind == JsonValueKind.Number &&
            sessionRevision.TryGetInt64(out var current) &&
            current == proposal.CreatedRevision
                ? ProposalFreshnessDecision.Fresh
                : ProposalFreshnessDecision.Stale;
    }
}

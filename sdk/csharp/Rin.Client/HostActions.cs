using System.Text.Json;

namespace Rin.Client;

// Engine-neutral construction helpers. They copy a game-authored offer into
// the invocation verbatim; callers still own final epoch, deadline, target,
// and game-rule validation immediately before execution.
public static class HostActions
{
    public static ActionOfferInput Offer(
        string offerId,
        string actorId,
        CapabilityRef capability,
        string descriptorDigest,
        string description,
        DecisionWindow window,
        JsonElement? arguments = null,
        IReadOnlyList<HostRef>? targets = null) =>
        new(
            offerId,
            window.Id,
            actorId,
            capability,
            descriptorDigest,
            description,
            arguments ?? JsonSerializer.SerializeToElement(new { }),
            window.Epoch,
            window.ObservationSeq,
            window.Deadline)
        {
            Targets = targets,
        };

    public static ActionInvocation Bind(
        string operationId,
        ActionOfferInput offer) =>
        new(
            operationId,
            offer.OfferId,
            offer.DecisionWindowId,
            offer.ActorId,
            offer.Capability,
            offer.DescriptorDigest,
            offer.Arguments,
            offer.ExpectedEpoch,
            offer.ObservationSeq,
            offer.Deadline)
        {
            Targets = offer.Targets,
        };

    public static ReportActionRequest ImmediateReport(
        string sessionId,
        string requestId,
        string eventId,
        long tick,
        ActionProposal proposal,
        string operationId,
        bool accepted,
        string summary,
        Epoch outcomeEpoch,
        ulong worldSeq,
        Timepoint occurredAt,
        IReadOnlyList<string>? tags = null)
    {
        Guard.NotNull(proposal, nameof(proposal));
        var report = new ActionReportInput(
            proposal.Id,
            eventId,
            accepted ? "accepted" : "rejected",
            summary)
        {
            Tags = tags,
        };
        if (accepted)
        {
            report = report with
            {
                Invocation = Bind(operationId, proposal.Action),
                Run = new ActionRun(
                    operationId,
                    "succeeded",
                    1,
                    100,
                    occurredAt),
                Outcome = new ActionOutcome(
                    operationId,
                    "succeeded",
                    summary,
                    outcomeEpoch,
                    worldSeq,
                    occurredAt),
            };
        }
        return new ReportActionRequest(
            sessionId,
            requestId,
            tick,
            report);
    }
}

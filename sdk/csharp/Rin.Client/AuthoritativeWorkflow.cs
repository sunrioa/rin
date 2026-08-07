using System.Text.Json;

namespace Rin.Client;

public sealed record ProposalAttempt(
    int Version,
    string OperationId,
    ProposeRequest Request,
    string JobId)
{
    public static ProposalAttempt Create(string operationId, ProposeRequest request) =>
        new(1, operationId, request, string.Empty);
}

public sealed record ResolvedProposalAttempt(
    ProposalAttempt Attempt,
    ActionProposal Proposal,
    bool Duplicate);

public interface IProposalAttemptStore
{
    ValueTask<ProposalAttempt?> LoadAsync(CancellationToken cancellationToken = default);

    /// <summary>
    /// Atomically creates the Attempt and returns false when one already exists.
    /// </summary>
    ValueTask<bool> CreateAsync(
        ProposalAttempt attempt,
        CancellationToken cancellationToken = default);

    /// <summary>Updates only the matching Attempt with its Job identity.</summary>
    ValueTask SaveAsync(
        ProposalAttempt attempt,
        CancellationToken cancellationToken = default);

    /// <summary>
    /// Atomically runs apply, persists the applied marker and exact Report in
    /// the Outcome Outbox, and removes the matching Proposal Attempt.
    /// </summary>
    ValueTask SettleAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        ReportActionRequest report,
        Func<CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default);
}

public sealed class ProposalAttemptCoordinator
{
    private readonly RinClient client;
    private readonly IProposalAttemptStore store;

    public ProposalAttemptCoordinator(RinClient client, IProposalAttemptStore store)
    {
        this.client = client ?? throw new ArgumentNullException(nameof(client));
        this.store = store ?? throw new ArgumentNullException(nameof(store));
    }

    public async ValueTask<ProposalAttempt> BeginAsync(
        string operationId,
        ProposeRequest request,
        CancellationToken cancellationToken = default)
    {
        Guard.NotNull(request, nameof(request));
        RequireIdentifier("operation_id", operationId);
        RequireIdentifier("request_id", request.RequestId);
        RequireIdentifier("session_id", request.SessionId);
        var attempt = ProposalAttempt.Create(operationId, Clone(request));
        if (!await store.CreateAsync(attempt, cancellationToken).ConfigureAwait(false))
        {
            throw new RinConfigurationException(
                "proposal_attempt_pending",
                "A Proposal Attempt is already pending");
        }
        return attempt;
    }

    public async ValueTask<ResolvedProposalAttempt> ResumeAsync(
        TimeSpan? deadline = null,
        TimeSpan? interval = null,
        CancellationToken cancellationToken = default)
    {
        var attempt = ValidateAttempt(
            await store.LoadAsync(cancellationToken).ConfigureAwait(false));
        JsonElement? job = null;
        if (attempt.JobId.Length != 0)
        {
            try
            {
                job = await client.WaitForProposalAsync(
                    attempt.JobId,
                    deadline,
                    interval,
                    cancellationToken).ConfigureAwait(false);
            }
            catch (RinApiException exception)
                when (exception.Code == "job_not_found")
            {
                // Job metadata is process-local. Re-submit the exact durable
                // request so Rin can reconstruct it from Session identity.
            }
        }

        if (job is null)
        {
            var submission = await client.SubmitProposalJobAsync(
                attempt.Request,
                cancellationToken).ConfigureAwait(false);
            var jobId = RequiredIdentifier(submission, "job_id");
            attempt = attempt with { JobId = jobId };
            await store.SaveAsync(attempt, cancellationToken).ConfigureAwait(false);
            job = await client.WaitForProposalAsync(
                jobId,
                deadline,
                interval,
                cancellationToken).ConfigureAwait(false);
        }

        try
        {
            var proposal = job.Value.GetProperty("proposal")
                .Deserialize<ActionProposal>() ??
                throw new JsonException("proposal was null");
            RequireResolvedProposalMatches(attempt, proposal);
            var duplicate = job.Value.TryGetProperty("duplicate", out var duplicateValue) &&
                duplicateValue.ValueKind == JsonValueKind.True;
            return new ResolvedProposalAttempt(attempt, proposal, duplicate);
        }
        catch (RinProtocolException)
        {
            throw;
        }
        catch (Exception exception)
            when (exception is JsonException or InvalidOperationException or KeyNotFoundException)
        {
            throw new RinProtocolException(
                "invalid_job",
                "Resolved Proposal does not match the typed protocol model",
                exception);
        }
    }

    public ValueTask SettleAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        ReportActionRequest report,
        Func<CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default)
    {
        attempt = ValidateAttempt(attempt);
        Guard.NotNull(apply, nameof(apply));
        ValidateSettlement(attempt, proposal, report);
        return store.SettleAsync(
            attempt,
            proposal,
            report,
            apply,
            cancellationToken);
    }

    internal static void ValidateSettlement(
        ProposalAttempt attempt,
        ActionProposal proposal,
        ReportActionRequest report)
    {
        attempt = ValidateAttempt(attempt);
        Guard.NotNull(proposal, nameof(proposal));
        Guard.NotNull(report, nameof(report));
        RequireResolvedProposalMatches(attempt, proposal);
        if (report.SessionId != attempt.Request.SessionId ||
            report.Report is null ||
            report.Report.ProposalId != proposal.Id)
        {
            throw new RinConfigurationException(
                "workflow_identity_mismatch",
                "Attempt, Proposal, and Report identities do not match");
        }
        RequireIdentifier("request_id", report.RequestId);
        RequireIdentifier("event_id", report.Report.EventId);
    }

    internal static void RequireResolvedProposalMatches(
        ProposalAttempt attempt,
        ActionProposal proposal)
    {
        attempt = ValidateAttempt(attempt);
        Guard.NotNull(proposal, nameof(proposal));
        var selectedAuthoredOffer = false;
        foreach (var offer in attempt.Request.Offers)
        {
            if (OffersEqual(offer, proposal.Action))
            {
                selectedAuthoredOffer = true;
                break;
            }
        }
        if (!RinIds.IsValid(proposal.Id) ||
            proposal.SessionId != attempt.Request.SessionId ||
            proposal.RequestId != attempt.Request.RequestId ||
            proposal.ActorId != attempt.Request.ActorId ||
            proposal.Tick != attempt.Request.Tick ||
            !DecisionWindowsEqual(
                proposal.DecisionWindow,
                attempt.Request.DecisionWindow) ||
            !selectedAuthoredOffer)
        {
            throw new RinProtocolException(
                "invalid_job",
                "Resolved Proposal does not match the durable Attempt");
        }
    }

    private static bool DecisionWindowsEqual(
        DecisionWindow left,
        DecisionWindow right)
    {
        if (left is null || right is null ||
            left.Id != right.Id ||
            left.Mode != right.Mode ||
            left.Epoch != right.Epoch ||
            left.ObservationSeq != right.ObservationSeq ||
            left.OpenedAt != right.OpenedAt ||
            left.Deadline != right.Deadline ||
            left.ActorIds.Count != right.ActorIds.Count)
        {
            return false;
        }
        for (var index = 0; index < left.ActorIds.Count; index++)
        {
            if (left.ActorIds[index] != right.ActorIds[index]) return false;
        }
        return true;
    }

    private static bool OffersEqual(ActionOfferInput left, ActionOfferInput right)
    {
        if (left is null || right is null ||
            left.OfferId != right.OfferId ||
            left.DecisionWindowId != right.DecisionWindowId ||
            left.ActorId != right.ActorId ||
            left.Capability != right.Capability ||
            left.DescriptorDigest != right.DescriptorDigest ||
            left.Description != right.Description ||
            left.ExpectedEpoch != right.ExpectedEpoch ||
            left.ObservationSeq != right.ObservationSeq ||
            left.Deadline != right.Deadline ||
            !PlanningEqual(left.Planning, right.Planning) ||
            !JsonValues.Equivalent(left.Arguments, right.Arguments))
        {
            return false;
        }
        var leftTargets = left.Targets ?? Array.Empty<HostRef>();
        var rightTargets = right.Targets ?? Array.Empty<HostRef>();
        if (leftTargets.Count != rightTargets.Count) return false;
        for (var index = 0; index < leftTargets.Count; index++)
        {
            if (leftTargets[index] != rightTargets[index]) return false;
        }
        return true;
    }

    private static bool PlanningEqual(
        ActionPlanMetadata? left,
        ActionPlanMetadata? right)
    {
        if (ReferenceEquals(left, right)) return true;
        if (left is null || right is null ||
            left.Intent != right.Intent ||
            left.PlanId != right.PlanId ||
            left.StepIndex != right.StepIndex ||
            left.PlanRevision != right.PlanRevision ||
            left.BlockedReason != right.BlockedReason ||
            left.Risk != right.Risk)
        {
            return false;
        }
        return StringListsEqual(left.Preconditions, right.Preconditions) &&
            StringListsEqual(left.Postconditions, right.Postconditions);
    }

    private static bool StringListsEqual(
        IReadOnlyList<string>? left,
        IReadOnlyList<string>? right)
    {
        if (ReferenceEquals(left, right)) return true;
        if (left is null || right is null || left.Count != right.Count) return false;
        for (var index = 0; index < left.Count; index++)
        {
            if (left[index] != right[index]) return false;
        }
        return true;
    }

    private static ProposalAttempt ValidateAttempt(ProposalAttempt? attempt)
    {
        if (attempt is null ||
            attempt.Version != 1 ||
            attempt.Request is null ||
            !RinIds.IsValid(attempt.OperationId) ||
            !RinIds.IsValid(attempt.Request.RequestId) ||
            !RinIds.IsValid(attempt.Request.SessionId) ||
            (attempt.JobId.Length != 0 && !RinIds.IsValid(attempt.JobId)))
        {
            throw new RinConfigurationException(
                "invalid_proposal_attempt",
                "Durable Proposal Attempt is missing or malformed");
        }
        return attempt;
    }

    private static string RequiredIdentifier(JsonElement value, string property)
    {
        if (!value.TryGetProperty(property, out var element) ||
            element.ValueKind != JsonValueKind.String ||
            !RinIds.IsValid(element.GetString()))
        {
            throw new RinProtocolException(
                "invalid_job",
                $"Rin returned an invalid {property}");
        }
        return element.GetString()!;
    }

    private static void RequireIdentifier(string field, string? value)
    {
        if (!RinIds.IsValid(value))
        {
            throw new RinConfigurationException(
                "invalid_workflow",
                $"{field} must be a protocol identifier");
        }
    }

    private static T Clone<T>(T value)
    {
        try
        {
            return JsonSerializer.Deserialize<T>(
                JsonSerializer.SerializeToUtf8Bytes(value)) ??
                throw new JsonException("workflow value was null");
        }
        catch (JsonException exception)
        {
            throw new RinConfigurationException(
                "invalid_workflow",
                "Workflow value is not a valid protocol object",
                exception);
        }
    }
}

public sealed record OutcomeOutboxEntry(
    string Key,
    ReportActionRequest Report);

public interface IOutcomeOutboxStore
{
    ValueTask<IReadOnlyList<OutcomeOutboxEntry>> ListAsync(
        CancellationToken cancellationToken = default);

    /// <summary>Durably removes only the exact entry after its ACK Session is verified.</summary>
    ValueTask AcknowledgeAsync(
        OutcomeOutboxEntry entry,
        MutationResult result,
        CancellationToken cancellationToken = default);
}

public sealed class OutcomeOutbox
{
    private readonly RinClient client;
    private readonly IOutcomeOutboxStore store;
    private int draining;

    public OutcomeOutbox(RinClient client, IOutcomeOutboxStore store)
    {
        this.client = client ?? throw new ArgumentNullException(nameof(client));
        this.store = store ?? throw new ArgumentNullException(nameof(store));
    }

    public async ValueTask<int> DrainAsync(
        CancellationToken cancellationToken = default)
    {
        if (Interlocked.Exchange(ref draining, 1) != 0)
        {
            throw new RinConfigurationException(
                "outbox_busy",
                "Outcome Outbox is already being drained");
        }
        try
        {
            var acknowledged = 0;
            var listed = await store.ListAsync(cancellationToken).ConfigureAwait(false) ??
                throw new RinConfigurationException(
                    "invalid_outbox",
                    "Outcome Outbox returned null");
            var entries = new List<OutcomeOutboxEntry>(listed);
            foreach (var entry in entries)
            {
                Guard.NotNull(entry, nameof(entry));
                var report = Guard.NotNull(entry.Report, nameof(entry.Report));
                if (!RinIds.IsValid(report.SessionId) ||
                    !RinIds.IsValid(report.RequestId) ||
                    report.Report is null ||
                    !RinIds.IsValid(report.Report.EventId))
                {
                    throw InvalidOutbox();
                }
                var result = await client.ReportActionAsync(report, cancellationToken)
                    .ConfigureAwait(false);
                if (!ValidMutationAcknowledgement(result, report.SessionId))
                {
                    throw new RinConfigurationException(
                        "invalid_outbox_ack",
                        "Rin returned a malformed or wrong-Session Outcome acknowledgement");
                }
                await store.AcknowledgeAsync(entry, result, cancellationToken)
                    .ConfigureAwait(false);
                acknowledged++;
            }
            return acknowledged;
        }
        finally
        {
            Volatile.Write(ref draining, 0);
        }
    }

    private static bool ValidMutationAcknowledgement(
        MutationResult? result,
        string expectedSessionId)
    {
        if (result is null ||
            !string.Equals(
                result.SessionId,
                expectedSessionId,
                StringComparison.Ordinal) ||
            !RinIds.IsValid(result.SessionId) ||
            result.Revision < 1 ||
            result.Revision > 9_007_199_254_740_991L ||
            result.HeadHash is null ||
            result.HeadHash.Length != 64 ||
            !result.DuplicateValue.HasValue)
        {
            return false;
        }
        foreach (var character in result.HeadHash)
        {
            var digit = character >= '0' && character <= '9';
            var lowerHex = character >= 'a' && character <= 'f';
            if (!digit && !lowerHex)
            {
                return false;
            }
        }
        return true;
    }

    private static RinConfigurationException InvalidOutbox() =>
        new(
            "invalid_outbox",
            "Outcome Outbox entry has invalid stable identities or no report request");

}

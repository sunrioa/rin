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
    /// Atomically runs apply, persists the applied marker and exact Commit in
    /// the Outcome Outbox, and removes the matching Proposal Attempt.
    /// </summary>
    ValueTask SettleAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        CommitRequest commit,
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
            if (proposal.SessionId != attempt.Request.SessionId ||
                proposal.RequestId != attempt.Request.RequestId)
            {
                throw new RinProtocolException(
                    "invalid_job",
                    "Resolved Proposal does not match the durable Attempt");
            }
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
        CommitRequest commit,
        Func<CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default)
    {
        attempt = ValidateAttempt(attempt);
        Guard.NotNull(apply, nameof(apply));
        ValidateSettlement(attempt, proposal, commit);
        return store.SettleAsync(
            attempt,
            proposal,
            commit,
            apply,
            cancellationToken);
    }

    internal static void ValidateSettlement(
        ProposalAttempt attempt,
        ActionProposal proposal,
        CommitRequest commit)
    {
        attempt = ValidateAttempt(attempt);
        Guard.NotNull(proposal, nameof(proposal));
        Guard.NotNull(commit, nameof(commit));
        if (proposal.SessionId != attempt.Request.SessionId ||
            proposal.RequestId != attempt.Request.RequestId ||
            commit.SessionId != attempt.Request.SessionId ||
            commit.ProposalId != proposal.Id)
        {
            throw new RinConfigurationException(
                "workflow_identity_mismatch",
                "Attempt, Proposal, and Commit identities do not match");
        }
        RequireIdentifier("request_id", commit.RequestId);
        RequireIdentifier("event_id", commit.EventId);
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

public sealed record OutcomeOutboxEntry
{
    public OutcomeOutboxEntry(string key, CommitRequest commit)
        : this(key, commit, null, false) { }

    public OutcomeOutboxEntry(
        string key,
        CommitRequest commit,
        object fallbackObserve)
        : this(key, commit, fallbackObserve, false) { }

    [System.Text.Json.Serialization.JsonConstructor]
    public OutcomeOutboxEntry(
        string key,
        CommitRequest commit,
        object? fallbackObserve,
        bool degradedObserve)
    {
        Key = key;
        Commit = commit;
        FallbackObserve = fallbackObserve;
        DegradedObserve = degradedObserve;
    }

    public string Key { get; init; }
    public CommitRequest Commit { get; init; }
    public object? FallbackObserve { get; init; }
    public bool DegradedObserve { get; init; }

    [System.Text.Json.Serialization.JsonIgnore]
    public bool IsDegradedObserve => DegradedObserve;

    public OutcomeOutboxEntry AsDegradedObserve()
    {
        if (FallbackObserve is null)
        {
            throw new RinConfigurationException(
                "outcome_fallback_missing",
                "Outcome has no pre-recorded safe Observe fallback");
        }
        return this with { DegradedObserve = true };
    }

    internal static JsonElement ValidateFallback(object value)
    {
        try
        {
            var element = value is JsonElement json
                ? json.Clone()
                : JsonSerializer.SerializeToElement(value);
            if (element.ValueKind != JsonValueKind.Object ||
                !ValidIdentifier(element, "session_id") ||
                !ValidIdentifier(element, "request_id") ||
                !ValidIdentifier(element, "event_id"))
            {
                throw new RinConfigurationException(
                    "invalid_outbox",
                    "Outcome Observe fallback has invalid stable identities");
            }
            return element;
        }
        catch (RinConfigurationException)
        {
            throw;
        }
        catch (Exception exception)
            when (exception is JsonException or NotSupportedException)
        {
            throw new RinConfigurationException(
                "invalid_outbox",
                "Outcome Observe fallback is not a valid JSON object",
                exception);
        }
    }

    private static bool ValidIdentifier(JsonElement value, string property) =>
        value.TryGetProperty(property, out var identifier) &&
        identifier.ValueKind == JsonValueKind.String &&
        RinIds.IsValid(identifier.GetString());
}

public interface IOutcomeOutboxStore
{
    ValueTask<IReadOnlyList<OutcomeOutboxEntry>> ListAsync(
        CancellationToken cancellationToken = default);

    /// <summary>Durably removes only the exact entry acknowledged by Rin.</summary>
    ValueTask AcknowledgeAsync(
        OutcomeOutboxEntry entry,
        MutationResult result,
        CancellationToken cancellationToken = default);
}

public interface IOutcomeFallbackStore : IOutcomeOutboxStore
{
    /// <summary>
    /// Atomically replaces an unrecoverable Commit with its pre-recorded,
    /// absolute-fact Observe fallback.
    /// </summary>
    ValueTask<OutcomeOutboxEntry> ReplaceWithFallbackAsync(
        OutcomeOutboxEntry entry,
        CancellationToken cancellationToken = default);
}

public sealed class OutcomeOutbox
{
    private static readonly HashSet<string> TerminalCommitErrors =
        new(StringComparer.Ordinal)
        {
            "session_not_found",
            "unknown_proposal",
            "proposal_resolved",
        };

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
            var entries = await store.ListAsync(cancellationToken).ConfigureAwait(false) ??
                throw new RinConfigurationException(
                    "invalid_outbox",
                    "Outcome Outbox returned null");
            foreach (var entry in entries)
            {
                Guard.NotNull(entry, nameof(entry));
                if (entry.IsDegradedObserve)
                {
                    if (entry.FallbackObserve is null)
                    {
                        throw InvalidOutbox();
                    }
                    var observed = await client.ObserveAsync(
                        OutcomeOutboxEntry.ValidateFallback(entry.FallbackObserve),
                        cancellationToken).ConfigureAwait(false);
                    await store.AcknowledgeAsync(
                        entry,
                        ParseMutation(observed),
                        cancellationToken)
                        .ConfigureAwait(false);
                    acknowledged++;
                    continue;
                }

                var commit = Guard.NotNull(entry.Commit, nameof(entry.Commit));
                if (!RinIds.IsValid(commit.RequestId) ||
                    !RinIds.IsValid(commit.EventId))
                {
                    throw InvalidOutbox();
                }
                try
                {
                    var result = await client.CommitAsync(commit, cancellationToken)
                        .ConfigureAwait(false);
                    await store.AcknowledgeAsync(entry, result, cancellationToken)
                        .ConfigureAwait(false);
                }
                catch (RinApiException exception)
                    when (TerminalCommitErrors.Contains(exception.Code) &&
                          entry.FallbackObserve is not null)
                {
                    if (store is not IOutcomeFallbackStore fallbackStore)
                    {
                        throw new RinConfigurationException(
                            "outcome_fallback_unsupported",
                            "Workflow Store cannot persist an Outcome fallback conversion",
                            exception);
                    }
                    var converted = await fallbackStore.ReplaceWithFallbackAsync(
                        entry,
                        cancellationToken).ConfigureAwait(false);
                    if (!converted.IsDegradedObserve ||
                        converted.FallbackObserve is null)
                    {
                        throw InvalidOutbox();
                    }
                    var observed = await client.ObserveAsync(
                        OutcomeOutboxEntry.ValidateFallback(converted.FallbackObserve),
                        cancellationToken).ConfigureAwait(false);
                    await store.AcknowledgeAsync(
                        converted,
                        ParseMutation(observed),
                        cancellationToken)
                        .ConfigureAwait(false);
                }
                acknowledged++;
            }
            return acknowledged;
        }
        finally
        {
            Volatile.Write(ref draining, 0);
        }
    }

    private static RinConfigurationException InvalidOutbox() =>
        new(
            "invalid_outbox",
            "Outcome Outbox entry has invalid stable identities or no report request");

    private static MutationResult ParseMutation(JsonElement value)
    {
        try
        {
            return value.Deserialize<MutationResult>() ??
                throw new JsonException("mutation result was null");
        }
        catch (JsonException exception)
        {
            throw new RinProtocolException(
                "invalid_response",
                "Observe returned an invalid mutation result",
                exception);
        }
    }
}

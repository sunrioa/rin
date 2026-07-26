namespace Rin.Client;

public sealed record PendingTurn(
    int Version,
    string OperationId,
    ProposeRequest Request,
    string JobId)
{
    internal static PendingTurn FromAttempt(ProposalAttempt attempt) =>
        new(attempt.Version, attempt.OperationId, attempt.Request, attempt.JobId);

    internal ProposalAttempt ToAttempt() =>
        new(Version, OperationId, Request, JobId);
}

public sealed record ResolvedPendingTurn(
    PendingTurn PendingTurn,
    ActionProposal Proposal,
    bool Duplicate);

public interface IWorkflowStore : IProposalAttemptStore, IOutcomeOutboxStore
{
    /// <summary>
    /// After an advisory or operation-keyed idempotent apply, atomically
    /// persists the marker and exact Commit, then removes the Pending Turn.
    /// This method must not apply the game effect.
    /// </summary>
    ValueTask CompleteAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        CommitRequest commit,
        CancellationToken cancellationToken = default);
}

public interface IWorkflowFallbackStore : IWorkflowStore, IOutcomeFallbackStore
{
    /// <summary>
    /// After apply, atomically persists the exact Commit and a safe
    /// absolute-fact Observe fallback, then removes the Pending Turn.
    /// </summary>
    ValueTask CompleteWithFallbackAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        CommitRequest commit,
        object fallbackObserve,
        CancellationToken cancellationToken = default);
}

public sealed class WorkflowCoordinator
{
    private readonly HostDurability durability;
    private readonly IWorkflowStore store;
    private readonly ProposalAttemptCoordinator attempts;
    private readonly OutcomeOutbox outbox;
    private int resuming;
    private int settling;

    public WorkflowCoordinator(
        RinClient client,
        IWorkflowStore store,
        HostDurability? durability = null)
    {
        Guard.NotNull(client, nameof(client));
        this.store = store ?? throw new ArgumentNullException(nameof(store));
        this.durability = (durability ?? HostDurability.Advisory()).Validate();
        attempts = new ProposalAttemptCoordinator(client, store);
        outbox = new OutcomeOutbox(client, store);
    }

    public HostDurability Durability => durability;

    public async ValueTask<PendingTurn> BeginAsync(
        string operationId,
        ProposeRequest request,
        CancellationToken cancellationToken = default) =>
        PendingTurn.FromAttempt(
            await attempts.BeginAsync(operationId, request, cancellationToken)
                .ConfigureAwait(false));

    public async ValueTask<ResolvedPendingTurn> ResumePendingWorkAsync(
        TimeSpan? deadline = null,
        TimeSpan? interval = null,
        CancellationToken cancellationToken = default)
    {
        if (Interlocked.Exchange(ref resuming, 1) != 0)
        {
            throw new RinConfigurationException(
                "workflow_busy",
                "Pending work is already being resumed");
        }
        try
        {
            await outbox.DrainAsync(cancellationToken).ConfigureAwait(false);
            var resolved = await attempts.ResumeAsync(deadline, interval, cancellationToken)
                .ConfigureAwait(false);
            return new ResolvedPendingTurn(
                PendingTurn.FromAttempt(resolved.Attempt),
                resolved.Proposal,
                resolved.Duplicate);
        }
        finally
        {
            Volatile.Write(ref resuming, 0);
        }
    }

    public async ValueTask ApplyAndEnqueueOutcomeAsync(
        PendingTurn pendingTurn,
        ActionProposal proposal,
        CommitRequest commit,
        HostDurabilityProfile requiredDurability,
        Func<string, CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default)
    {
        Guard.NotNull(pendingTurn, nameof(pendingTurn));
        Guard.NotNull(apply, nameof(apply));
        if (Interlocked.Exchange(ref settling, 1) != 0)
        {
            throw new RinConfigurationException(
                "workflow_busy",
                "A Pending Turn is already being settled");
        }
        try
        {
            durability.Require(requiredDurability);
            var attempt = pendingTurn.ToAttempt();
            ProposalAttemptCoordinator.ValidateSettlement(attempt, proposal, commit);
            if (durability.Profile == HostDurabilityProfile.TransactionalAction)
            {
                await attempts.SettleAsync(
                    attempt,
                    proposal,
                    commit,
                    token => apply(attempt.OperationId, token),
                    cancellationToken).ConfigureAwait(false);
                return;
            }

            await apply(attempt.OperationId, cancellationToken).ConfigureAwait(false);
            await store.CompleteAsync(
                attempt,
                proposal,
                commit,
                cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            Volatile.Write(ref settling, 0);
        }
    }

    public async ValueTask ApplyAndEnqueueOutcomeWithFallbackAsync(
        PendingTurn pendingTurn,
        ActionProposal proposal,
        CommitRequest commit,
        object fallbackObserve,
        HostDurabilityProfile requiredDurability,
        Func<string, CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default)
    {
        Guard.NotNull(fallbackObserve, nameof(fallbackObserve));
        var safeFallback = OutcomeOutboxEntry.ValidateFallback(fallbackObserve);
        if (store is not IWorkflowFallbackStore fallbackStore)
        {
            throw new RinConfigurationException(
                "outcome_fallback_unsupported",
                "Workflow Store cannot persist safe Outcome fallbacks");
        }
        Guard.NotNull(pendingTurn, nameof(pendingTurn));
        Guard.NotNull(apply, nameof(apply));
        if (Interlocked.Exchange(ref settling, 1) != 0)
        {
            throw new RinConfigurationException(
                "workflow_busy",
                "A Pending Turn is already being settled");
        }
        try
        {
            durability.Require(requiredDurability);
            var attempt = pendingTurn.ToAttempt();
            ProposalAttemptCoordinator.ValidateSettlement(attempt, proposal, commit);
            if (durability.Profile == HostDurabilityProfile.TransactionalAction)
            {
                throw new RinConfigurationException(
                    "outcome_fallback_unsupported",
                    "Transactional stores must define fallback settlement in their transaction");
            }
            await apply(attempt.OperationId, cancellationToken).ConfigureAwait(false);
            await fallbackStore.CompleteWithFallbackAsync(
                attempt,
                proposal,
                commit,
                safeFallback,
                cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            Volatile.Write(ref settling, 0);
        }
    }

    public ValueTask<int> DrainOutboxAsync(
        CancellationToken cancellationToken = default) =>
        outbox.DrainAsync(cancellationToken);
}

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

public sealed class WorkflowCoordinator
{
    private readonly HostCapabilities capabilities;
    private readonly IWorkflowStore store;
    private readonly ProposalAttemptCoordinator attempts;
    private readonly OutcomeOutbox outbox;
    private int resuming;
    private int settling;

    public WorkflowCoordinator(
        RinClient client,
        IWorkflowStore store,
        HostCapabilities? capabilities = null)
    {
        ArgumentNullException.ThrowIfNull(client);
        this.store = store ?? throw new ArgumentNullException(nameof(store));
        this.capabilities = (capabilities ?? HostCapabilities.Advisory()).Validate();
        attempts = new ProposalAttemptCoordinator(client, store);
        outbox = new OutcomeOutbox(client, store);
    }

    public HostCapabilities Capabilities => capabilities;

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
        HostProfile requiredProfile,
        Func<string, CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(pendingTurn);
        ArgumentNullException.ThrowIfNull(apply);
        if (Interlocked.Exchange(ref settling, 1) != 0)
        {
            throw new RinConfigurationException(
                "workflow_busy",
                "A Pending Turn is already being settled");
        }
        try
        {
            capabilities.Require(requiredProfile);
            var attempt = pendingTurn.ToAttempt();
            ProposalAttemptCoordinator.ValidateSettlement(attempt, proposal, commit);
            if (capabilities.Profile == HostProfile.TransactionalAction)
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

    public ValueTask<int> DrainOutboxAsync(
        CancellationToken cancellationToken = default) =>
        outbox.DrainAsync(cancellationToken);
}

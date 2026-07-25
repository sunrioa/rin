using System.Security.Cryptography;
using System.Text;
using Rin.Client;

namespace RinNpcExample;

public interface IRinNpcHost
{
    long CurrentTick { get; }
    Task<bool> ApplyDialogueAsync(
        string actionId,
        string authoredLine,
        CancellationToken cancellationToken);
    void Log(string message, bool error = false);
}

public sealed class RinNpcRuntime : IDisposable
{
    private const string ActorId = "npc.rin.companion";
    private static readonly HashSet<string> AllowedActions =
        new(StringComparer.Ordinal) { "talk", "wait", "refuse" };

    private readonly IRinNpcHost host;
    private readonly BepInExWorkflowState store;
    private readonly RinClient client;
    private readonly WorkflowCoordinator workflow;
    private readonly SemaphoreSlim turns = new(1, 1);

    public RinNpcRuntime(
        IRinNpcHost host,
        BepInExWorkflowState store,
        string baseUrl,
        string token)
    {
        this.host = host;
        this.store = store;
        client = new RinClient(new RinClientOptions
        {
            BaseUrl = baseUrl,
            Token = token,
        });
        workflow = new WorkflowCoordinator(
            client,
            store,
            HostCapabilities.Advisory(stableIdentity: true));
    }

    public async Task RequestTurnAsync(
        string observation,
        long observedTick,
        CancellationToken cancellationToken = default)
    {
        if (!await turns.WaitAsync(0, cancellationToken).ConfigureAwait(false))
        {
            host.Log("A Rin turn is already running.", error: true);
            return;
        }
        try
        {
            var sessionId = store.SessionId;
            var next = store.Sequence + 1;
            var operationId = sessionId + "." + next;
            var create = Create(sessionId);
            var observe = new
            {
                protocol_version = RinClient.ProtocolVersion,
                session_id = sessionId,
                request_id = "observe." + operationId,
                event_id = "question." + operationId,
                tick = observedTick,
                observer_ids = new[] { ActorId },
                source = "bepinex-example",
                kind = "dialogue",
                summary = observation,
                tags = new[] { "conversation", "player-request" },
                importance = 3,
            };
            var propose = new ProposeRequest(
                sessionId,
                "propose." + operationId,
                ActorId,
                "Choose one bounded response to the player.",
                new[]
                {
                    new ActionSpecInput("talk", "dialogue", "offer one concrete hint"),
                    new ActionSpecInput("wait", "wait", "ask the player to observe first"),
                    new ActionSpecInput("refuse", "refuse", "decline an unsafe request"),
                })
            {
                Tick = checked(observedTick + 1),
                Tags = new[] { "conversation" },
            };
            if (await store.LoadAsync(cancellationToken).ConfigureAwait(false) is null)
            {
                store.StageTurnContext(create, observe);
                await workflow.BeginAsync(operationId, propose, cancellationToken)
                    .ConfigureAwait(false);
            }

            await client.CreateSessionAsync(store.CreateRequest!, cancellationToken)
                .ConfigureAwait(false);
            await workflow.DrainOutboxAsync(cancellationToken).ConfigureAwait(false);
            await client.ObserveAsync(store.PendingObserve!, cancellationToken)
                .ConfigureAwait(false);
            var resolved = await workflow.ResumePendingWorkAsync(
                cancellationToken: cancellationToken).ConfigureAwait(false);

            ProposalFreshnessDecision freshness;
            try
            {
                var state = await client.StateAsync(
                    new SessionRequest(sessionId),
                    cancellationToken).ConfigureAwait(false);
                freshness = ProposalFreshness.Evaluate(state, resolved.Proposal);
            }
            catch (RinException)
            {
                freshness = ProposalFreshnessDecision.Stale;
            }

            var actionId = resolved.Proposal.Action.Id;
            var allowed = freshness == ProposalFreshnessDecision.Fresh &&
                AllowedActions.Contains(actionId);
            var line = actionId switch
            {
                "talk" => "Companion: Check the terrain, then choose a route with cover.",
                "wait" => "Companion: Let us observe one more cycle before acting.",
                "refuse" => "Companion: I cannot help with actions that break the rules.",
                _ => string.Empty,
            };
            var commit = new CommitRequest(
                sessionId,
                "commit." + resolved.PendingTurn.OperationId,
                resolved.Proposal.Id,
                "outcome." + resolved.PendingTurn.OperationId,
                allowed)
            {
                Tick = Math.Max(host.CurrentTick, resolved.Proposal.Tick),
                Outcome = allowed
                    ? line
                    : "The game rejected a stale, unavailable, or non-allowlisted action.",
                Tags = new[] { "bepinex-example", "conversation" },
            };
            var fallbackObserve = new
            {
                protocol_version = RinClient.ProtocolVersion,
                session_id = sessionId,
                request_id = "outcome.observe." + resolved.PendingTurn.OperationId,
                event_id = commit.EventId,
                tick = commit.Tick,
                observer_ids = new[] { ActorId },
                source = "bepinex-example",
                kind = "action_outcome",
                summary = commit.Outcome,
                tags = new[] { "conversation", "outcome", allowed ? "applied" : "rejected" },
                importance = 3,
            };
            await workflow.ApplyAndEnqueueOutcomeWithFallbackAsync(
                resolved.PendingTurn,
                resolved.Proposal,
                commit,
                fallbackObserve,
                HostProfile.Advisory,
                async (_, token) =>
                {
                    if (allowed &&
                        !await host.ApplyDialogueAsync(actionId, line, token)
                            .ConfigureAwait(false))
                    {
                        throw new InvalidOperationException(
                            "The game did not apply its planned dialogue action");
                    }
                },
                cancellationToken).ConfigureAwait(false);
            await workflow.DrainOutboxAsync(cancellationToken).ConfigureAwait(false);
            host.Log("Rin outcome acknowledged.");
        }
        catch (Exception exception)
        {
            host.Log(
                "Rin did not apply a new action; retained work will resume: " +
                (exception is RinException rin ? rin.Code : "host_error"),
                error: true);
        }
        finally
        {
            turns.Release();
        }
    }

    public void Dispose()
    {
        client.Dispose();
    }

    private static CreateSessionRequest Create(string sessionId) =>
        new(
            "create." + sessionId,
            sessionId,
            new RinBinding(
                "unity-bepinex",
                "rin-npc-example",
                "0.6.0",
                "sha256:" + new string('0', 64)),
            new[]
            {
                new ActorSeedInput(ActorId, "npc", "Rin Companion", 20)
                {
                    Traits = new[] { "observant", "careful" },
                    Enabled = true,
                },
            })
        {
            Seed = StableSeed(sessionId),
            Features = new[] { RinFeatures.OutcomeReporting },
        };

    private static uint StableSeed(string sessionId)
    {
        using var sha256 = SHA256.Create();
        var digest = sha256.ComputeHash(Encoding.UTF8.GetBytes(sessionId));
        return (uint)(
            digest[0] |
            digest[1] << 8 |
            digest[2] << 16 |
            digest[3] << 24);
    }
}

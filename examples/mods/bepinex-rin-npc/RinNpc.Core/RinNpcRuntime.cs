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
    private static readonly HashSet<string> AllowedOffers =
        new(StringComparer.Ordinal)
        {
            "offer.talk", "offer.wait", "offer.refuse",
            "offer.quest", "offer.advance-quest",
        };

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
            HostDurability.Advisory(stableIdentity: true));
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
            var epoch = Epoch(sessionId);
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
                summary = observation + " Quest stage is " + store.QuestStage + ".",
                tags = new[] { "conversation", "player-request", "quest-stage-" + store.QuestStage },
                importance = 3,
                epoch,
                observation_seq = (ulong)observedTick,
            };
            var decisionTick = checked(observedTick + 1);
            var window = new DecisionWindow(
                "window." + operationId,
                "sequential",
                epoch,
                (ulong)observedTick,
                new Timepoint("realtime", decisionTick),
                new Timepoint("realtime", checked(decisionTick + 1)),
                new[] { ActorId });
            var propose = new ProposeRequest(
                sessionId,
                "propose." + operationId,
                ActorId,
                "Choose one bounded response to the player.",
                window,
                new[]
                {
                    Offer("offer.talk", "dialogue.talk", "offer one concrete hint", window),
                    Offer("offer.wait", "world.wait", "ask the player to observe first", window),
                    Offer("offer.refuse", "dialogue.refuse", "decline an unsafe request", window),
                    Offer("offer.quest", "quest.offer", "offer the authored beacon quest", window),
                    Offer("offer.advance-quest", "quest.advance", "mark the beacon quest complete", window),
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

            var actionId = resolved.Proposal.Action.OfferId;
            var allowed = freshness == ProposalFreshnessDecision.Fresh &&
                AllowedOffers.Contains(actionId) &&
                (actionId != "offer.quest" || store.QuestStage == 0) &&
                (actionId != "offer.advance-quest" || store.QuestStage == 1);
            var line = actionId switch
            {
                "offer.talk" => "Companion: Check the terrain, then choose a route with cover.",
                "offer.wait" => "Companion: Let us observe one more cycle before acting.",
                "offer.refuse" => "Companion: I cannot help with actions that break the rules.",
                "offer.quest" => "Companion: Find the ridge beacon and report back.",
                "offer.advance-quest" => "Companion: The beacon is secure; the route is now open.",
                _ => string.Empty,
            };
            var tick = Math.Max(host.CurrentTick, resolved.Proposal.Tick);
            var summary = allowed
                ? line
                : "The game rejected a stale, unavailable, or non-allowlisted action.";
            var report = HostActions.ImmediateReport(
                sessionId,
                "report." + resolved.PendingTurn.OperationId,
                "outcome." + resolved.PendingTurn.OperationId,
                tick,
                resolved.Proposal,
                resolved.PendingTurn.OperationId,
                allowed,
                summary,
                epoch,
                (ulong)tick,
                new Timepoint("realtime", tick),
                new[] { "bepinex-example", "conversation" });
            await workflow.ApplyAndEnqueueOutcomeAsync(
                resolved.PendingTurn,
                resolved.Proposal,
                report,
                HostDurabilityProfile.Advisory,
                async (_, token) =>
                {
                    if (allowed && (actionId == "offer.quest" || actionId == "offer.advance-quest"))
                    {
                        if (!store.ApplyQuestEffect(
                            resolved.PendingTurn.OperationId,
                            actionId))
                        {
                            throw new InvalidOperationException(
                                "The game rejected an invalid quest transition");
                        }
                    }
                    else if (allowed &&
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

    public void Dispose() => client.Dispose();

    private static CreateSessionRequest Create(string sessionId) =>
        new(
            "create." + sessionId,
            sessionId,
            new RinBinding(
                "unity-bepinex",
                "rin-npc-example",
                "0.7.0",
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
            Features = RinFeatures.SafeBaselinePreset,
        };

    private static Epoch Epoch(string sessionId) =>
        new(sessionId, "unity.game", 1, 1, 1);

    private static ActionOfferInput Offer(
        string offerId,
        string capabilityId,
        string description,
        DecisionWindow window) =>
        HostActions.Offer(
            offerId,
            ActorId,
            new CapabilityRef(capabilityId, "1"),
            new string('a', 64),
            description,
            window);
}

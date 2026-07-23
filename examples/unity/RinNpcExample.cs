using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.Reflection;
using UnityEngine;

public sealed class RinNpcExample : MonoBehaviour
{
    private const long NpcThinkEveryTicks = 5;

    [SerializeField] private RinClient rin;

    private readonly Dictionary<string, AppliedAction> appliedOperations =
        new Dictionary<string, AppliedAction>();
    private readonly Dictionary<string, PendingReport> reportOutbox =
        new Dictionary<string, PendingReport>();
    private readonly Dictionary<string, ProposalAttempt> proposalAttempts =
        new Dictionary<string, ProposalAttempt>();
    private string runId;
    private long operationSequence;
    private long lastAuthoritativeTick;
    private CreateSessionRequest createRequest;
    private bool authoritativeStateReady;
    private bool turnRunning;

    private void Awake()
    {
        // Recovery is a startup gate. A load error is not an empty save, and
        // no new identity or turn may exist until initialization is durable.
        authoritativeStateReady = RestoreAuthoritativeState();
        if (!authoritativeStateReady)
        {
            Debug.LogError(
                "Authoritative Rin state could not be restored; NPC turns are disabled.");
        }
    }

    public void AskNpcToRespond()
    {
        if (!authoritativeStateReady)
        {
            Debug.LogError(
                "Rin NPC turn refused until authoritative state recovery succeeds.");
            return;
        }
        if (turnRunning)
        {
            Debug.LogWarning("A Rin NPC turn is already running.");
            return;
        }
        turnRunning = true;
        StartCoroutine(RunNpcTurn());
    }

    private IEnumerator RunNpcTurn()
    {
        try
        {
            yield return ProposeAndApply();
        }
        finally
        {
            turnRunning = false;
        }
    }

    private IEnumerator ProposeAndApply()
    {
        if (!authoritativeStateReady) yield break;
        var sessionId = "playthrough." + runId;
        var resuming = proposalAttempts.TryGetValue(sessionId, out var attempt);
        // Keep this complete request stable. A lost response is retried on the
        // next turn with the same request ID and game-owned fields.
        MutationResult created = null;
        yield return rin.CreateSession(createRequest, value => created = value);
        if (created == null)
        {
            Debug.LogWarning(resuming
                ? "Rin create unavailable; the persisted Proposal attempt will fail closed."
                : "Rin create unavailable; an empty Outbox may use the authored fallback.");
        }

        // Every authoritative entry retries pending Commit or fallback Observe
        // reports before proposing or applying another action.
        var pendingReported = false;
        yield return FlushReportOutbox(value => pendingReported = value);
        if (!pendingReported) yield break;

        if (!resuming)
        {
            if (operationSequence == long.MaxValue)
            {
                Debug.LogError(
                    "Operation sequence exhausted; no new Proposal can be identified safely.");
                yield break;
            }
            if (!TryAllocateFreshProposalTick(out var newGameTick))
            {
                Debug.LogError("Authoritative tick exhausted; no new Proposal was submitted.");
                yield break;
            }
            var nextSequence = operationSequence + 1;
            var newOperationId =
                runId + "." + nextSequence.ToString(CultureInfo.InvariantCulture);
            var stableRequest = BuildProposeRequest(
                sessionId,
                newOperationId,
                newGameTick);
            attempt = new ProposalAttempt(
                newOperationId,
                nextSequence,
                stableRequest,
                "wait",
                "");
            // Persist the complete stable request, operation ID, and consumed
            // sequence before the first POST can create a Proposal Job.
            if (!PersistNewProposalAttempt(
                sessionId,
                attempt,
                nextSequence,
                newGameTick))
            {
                Debug.LogError(
                    "Could not durably save the Proposal attempt; nothing was submitted.");
                yield break;
            }
            proposalAttempts.Add(sessionId, attempt);
            operationSequence = nextSequence;
            lastAuthoritativeTick = newGameTick;
        }
        else
        {
            operationSequence = Math.Max(operationSequence, attempt.sequence);
        }

        var operationId = attempt.operationId;
        var request = attempt.request;
        AdapterResult result = null;
        yield return rin.ProposeWithFallback(
            request,
            attempt.fallbackActionId,
            value => result = value,
            allowOfflineBeforeSubmit: !resuming && created == null,
            knownJobId: attempt.jobId,
            persistJobId: jobId => RecordProposalJobId(
                sessionId,
                operationId,
                jobId));
        if (result == null || result.proposal == null) yield break;
        if (result.proposal.tick < 0)
        {
            Debug.LogError("Proposal tick is not a non-negative protocol integer.");
            yield break;
        }

        var planned = PlanActionInGame(result.proposal.action);
        PendingReport report;
        if (result.committable)
        {
            ProposalFreshnessResult freshness = null;
            yield return rin.ProposalFreshness(
                new SessionRequest { session_id = sessionId },
                result.proposal.id,
                value => freshness = value);
            if (freshness == null)
            {
                // We already have an online proposal. Reject it authoritatively;
                // never reinterpret a read failure as permission for fallback.
                planned = new AppliedAction(
                    result.proposal.action != null ? result.proposal.action.id : "",
                    false,
                    "The game rejected the proposal because freshness could not be verified.");
            }
            else if (!ProposalIsFresh(freshness, result.proposal, request))
            {
                planned = new AppliedAction(
                    result.proposal.action != null ? result.proposal.action.id : "",
                    false,
                    "The game rejected a stale proposal before applying any effect.");
            }
            report = BuildCommitReport(
                request.session_id,
                operationId,
                result.proposal.id,
                0,
                planned);
        }
        else
        {
            // Authored local fallbacks have no Rin Proposal to Commit. Reconcile
            // the game effect as a stable Observe with these exact IDs and tick.
            report = PendingReport.Observe(BuildFallbackObserveRequest(
                request.session_id,
                operationId,
                0,
                planned));
        }
        if (ApplyAndEnqueueAuthoritativeOperation(
            sessionId,
            operationId,
            planned,
            report,
            result.proposal.tick) == null)
            yield break;
        yield return FlushReportOutbox(_ => { });
    }

    private bool RestoreAuthoritativeState()
    {
        var loaded = LoadAuthoritativeState();
        if (loaded == null)
        {
            Debug.LogError("Authoritative state loader returned no result.");
            return false;
        }
        if (loaded.status == AuthoritativeStateLoadStatus.Loaded)
        {
            if (loaded.state == null || !TryHydrateAuthoritativeState(loaded.state))
            {
                Debug.LogError(
                    "Persisted authoritative state is missing, corrupt, or inconsistent.");
                return false;
            }
            return true;
        }
        if (loaded.status != AuthoritativeStateLoadStatus.NotFound)
        {
            Debug.LogError(
                "Authoritative state load failed: " + (loaded.error ?? "unknown"));
            return false;
        }

        // Only a positive NotFound result may mint a new identity. Save the
        // complete initialized object before exposing it to the running scene.
        var newRunId = Guid.NewGuid().ToString("N");
        var initialized = new AuthoritativeState
        {
            schemaVersion = 2,
            runId = newRunId,
            operationSequence = 0,
            lastAuthoritativeTick = 0,
            createRequest = BuildCreateRequest(newRunId),
            proposalAttempts = new ProposalAttemptState[0],
            appliedOperations = new AppliedOperationState[0],
            reportOutbox = new PendingReportState[0],
        };
        if (!PersistAuthoritativeStateInitialization(initialized))
        {
            Debug.LogError("Could not durably initialize authoritative state.");
            return false;
        }
        return TryHydrateAuthoritativeState(initialized);
    }

    private AuthoritativeStateLoadResult LoadAuthoritativeState()
    {
        // PRODUCTION RESTORE HOOK: synchronously deserialize one
        // AuthoritativeState and return Loaded(state), NotFound() only when
        // storage positively confirms absence, or Failed(error). Never map an
        // I/O, parse, or schema-version error to NotFound. This example remains
        // disabled until the game wires its save provider.
        return AuthoritativeStateLoadResult.Failed("restore hook not configured");
    }

    private bool PersistAuthoritativeStateInitialization(AuthoritativeState state)
    {
        // PRODUCTION PERSISTENCE HOOK: atomically create-if-absent this complete
        // serializable state, including sequence and high-water tick. A racing
        // existing row or uncertain write must return false so startup remains
        // closed.
        return true;
    }

    private bool TryHydrateAuthoritativeState(AuthoritativeState state)
    {
        if (state == null ||
            state.schemaVersion != 2 ||
            string.IsNullOrEmpty(state.runId) ||
            state.operationSequence < 0 ||
            state.lastAuthoritativeTick < 0 ||
            state.createRequest == null ||
            state.proposalAttempts == null ||
            state.appliedOperations == null ||
            state.reportOutbox == null)
            return false;

        var expectedSessionId = "playthrough." + state.runId;
        var expectedCreateRequest = BuildCreateRequest(state.runId);
        if (state.createRequest.session_id != expectedSessionId ||
            state.createRequest.request_id != "create." + state.runId ||
            !SemanticDtoEquals(state.createRequest, expectedCreateRequest))
            return false;

        var restoredAttempts = new Dictionary<string, ProposalAttempt>();
        foreach (var saved in state.proposalAttempts)
        {
            if (saved == null ||
                saved.sessionId != expectedSessionId ||
                string.IsNullOrEmpty(saved.operationId) ||
                saved.sequence <= 0 ||
                saved.sequence != state.operationSequence ||
                !TryParseOperationSequence(
                    saved.operationId,
                    state.runId,
                    out var attemptOperationSequence) ||
                attemptOperationSequence != saved.sequence ||
                saved.request == null ||
                saved.request.session_id != expectedSessionId ||
                saved.request.request_id != "propose." + saved.operationId ||
                !SemanticDtoEquals(
                    saved.request,
                    BuildProposeRequest(
                        expectedSessionId,
                        saved.operationId,
                        saved.request.tick)) ||
                saved.request.tick < 0 ||
                saved.request.tick > state.lastAuthoritativeTick ||
                saved.fallbackActionId != "wait" ||
                saved.jobId == null ||
                (saved.jobId.Length > 0 && !RinClient.IsProtocolId(saved.jobId)) ||
                !SemanticDtoEquals(saved, new ProposalAttemptState
                {
                    sessionId = expectedSessionId,
                    operationId = saved.operationId,
                    sequence = saved.sequence,
                    request = BuildProposeRequest(
                        expectedSessionId,
                        saved.operationId,
                        saved.request.tick),
                    fallbackActionId = "wait",
                    jobId = saved.jobId,
                }) ||
                restoredAttempts.ContainsKey(saved.sessionId))
                return false;
            restoredAttempts.Add(
                saved.sessionId,
                new ProposalAttempt(
                    saved.operationId,
                    saved.sequence,
                    saved.request,
                    saved.fallbackActionId,
                    saved.jobId ?? ""));
        }

        var restoredApplied = new Dictionary<string, AppliedAction>();
        foreach (var saved in state.appliedOperations)
        {
            if (saved == null ||
                string.IsNullOrEmpty(saved.operationId) ||
                !TryParseOperationSequence(
                    saved.operationId,
                    state.runId,
                    out var appliedOperationSequence) ||
                appliedOperationSequence > state.operationSequence ||
                saved.actionId == null ||
                saved.outcome == null ||
                !SemanticDtoEquals(saved, new AppliedOperationState
                {
                    operationId = saved.operationId,
                    actionId = saved.actionId,
                    accepted = saved.accepted,
                    outcome = saved.outcome,
                }) ||
                restoredApplied.ContainsKey(saved.operationId))
                return false;
            restoredApplied.Add(
                saved.operationId,
                new AppliedAction(saved.actionId ?? "", saved.accepted, saved.outcome ?? ""));
        }

        var restoredOutbox = new Dictionary<string, PendingReport>();
        foreach (var saved in state.reportOutbox)
        {
            if (saved == null ||
                string.IsNullOrEmpty(saved.operationId) ||
                !TryParseOperationSequence(
                    saved.operationId,
                    state.runId,
                    out var outboxOperationSequence) ||
                outboxOperationSequence > state.operationSequence ||
                !restoredApplied.ContainsKey(saved.operationId) ||
                (saved.kind != "commit" && saved.kind != "observe"))
                return false;
            PendingReport pending;
            if (saved.kind == "commit")
            {
                if (saved.commit == null ||
                    saved.fallback == null ||
                    saved.observe != null ||
                    saved.commit.request_id != "commit." + saved.operationId ||
                    saved.commit.event_id != "outcome." + saved.operationId ||
                    !RinClient.IsProtocolId(saved.commit.proposal_id) ||
                    saved.commit.session_id != expectedSessionId ||
                    saved.fallback.request_id != "reconcile." + saved.operationId ||
                    saved.fallback.session_id != saved.commit.session_id ||
                    saved.fallback.event_id != saved.commit.event_id ||
                    saved.commit.tick < 0 ||
                    saved.commit.tick > state.lastAuthoritativeTick ||
                    saved.fallback.tick != saved.commit.tick ||
                    saved.commit.accepted != restoredApplied[saved.operationId].accepted ||
                    saved.commit.outcome != restoredApplied[saved.operationId].outcome ||
                    !SemanticDtoEquals(
                        saved.commit,
                        BuildCommitRequest(
                            expectedSessionId,
                            saved.operationId,
                            saved.commit.proposal_id,
                            saved.commit.tick,
                            restoredApplied[saved.operationId])) ||
                    !OutcomeObserveMatchesApplied(
                        saved.fallback,
                        restoredApplied[saved.operationId],
                        saved.operationId,
                        expectedSessionId))
                    return false;
                pending = PendingReport.Commit(saved.commit, saved.fallback);
            }
            else
            {
                if (saved.observe == null ||
                    saved.commit != null ||
                    saved.fallback != null ||
                    saved.observe.session_id != expectedSessionId ||
                    saved.observe.request_id != "reconcile." + saved.operationId ||
                    (saved.observe.event_id != "fallback." + saved.operationId &&
                        saved.observe.event_id != "outcome." + saved.operationId) ||
                    saved.observe.tick < 0 ||
                    saved.observe.tick > state.lastAuthoritativeTick ||
                    !OutcomeObserveMatchesApplied(
                        saved.observe,
                        restoredApplied[saved.operationId],
                        saved.operationId,
                        expectedSessionId))
                    return false;
                pending = PendingReport.Observe(saved.observe);
            }
            if (restoredOutbox.ContainsKey(saved.operationId)) return false;
            restoredOutbox.Add(saved.operationId, pending);
        }
        foreach (var attempt in restoredAttempts.Values)
        {
            if (restoredApplied.ContainsKey(attempt.operationId) ||
                restoredOutbox.ContainsKey(attempt.operationId))
                return false;
        }

        runId = state.runId;
        operationSequence = state.operationSequence;
        lastAuthoritativeTick = state.lastAuthoritativeTick;
        createRequest = state.createRequest;
        proposalAttempts.Clear();
        appliedOperations.Clear();
        reportOutbox.Clear();
        foreach (var entry in restoredAttempts) proposalAttempts.Add(entry.Key, entry.Value);
        foreach (var entry in restoredApplied) appliedOperations.Add(entry.Key, entry.Value);
        foreach (var entry in restoredOutbox) reportOutbox.Add(entry.Key, entry.Value);
        return true;
    }

    private static bool TryParseOperationSequence(
        string operationId,
        string stableRunId,
        out long sequence)
    {
        sequence = 0;
        var prefix = stableRunId + ".";
        if (string.IsNullOrEmpty(operationId) ||
            !operationId.StartsWith(prefix, StringComparison.Ordinal))
            return false;
        var suffix = operationId.Substring(prefix.Length);
        return long.TryParse(
                suffix,
                NumberStyles.None,
                CultureInfo.InvariantCulture,
                out sequence) &&
            sequence > 0 &&
            suffix == sequence.ToString(CultureInfo.InvariantCulture);
    }

    private static bool OutcomeObserveMatchesApplied(
        ObserveRequest observe,
        AppliedAction applied,
        string operationId,
        string sessionId)
    {
        if (observe == null || applied == null || observe.source != "unity-example")
            return false;
        if (observe.event_id == "outcome." + operationId)
        {
            return observe.kind == "action_outcome" &&
                observe.summary == "Authoritative outcome: " + applied.outcome &&
                SemanticDtoEquals(
                    observe,
                    BuildOutcomeObserveRequest(
                        sessionId,
                        operationId,
                        observe.tick,
                        applied));
        }
        if (observe.event_id == "fallback." + operationId)
        {
            return observe.kind == "fallback_action" &&
                observe.summary ==
                    "Local fallback " + applied.actionId + ": " + applied.outcome &&
                SemanticDtoEquals(
                    observe,
                    BuildFallbackObserveRequest(
                        sessionId,
                        operationId,
                        observe.tick,
                        applied));
        }
        return false;
    }

    private static PendingReport BuildCommitReport(
        string sessionId,
        string operationId,
        string proposalId,
        long tick,
        AppliedAction applied)
    {
        return PendingReport.Commit(
            BuildCommitRequest(sessionId, operationId, proposalId, tick, applied),
            BuildOutcomeObserveRequest(sessionId, operationId, tick, applied));
    }

    private static CommitRequest BuildCommitRequest(
        string sessionId,
        string operationId,
        string proposalId,
        long tick,
        AppliedAction applied)
    {
        return new CommitRequest
        {
            protocol_version = RinClient.ProtocolVersion,
            session_id = sessionId,
            request_id = "commit." + operationId,
            proposal_id = proposalId,
            event_id = "outcome." + operationId,
            tick = tick,
            accepted = applied.accepted,
            outcome = applied.outcome,
            // Explicit nulls are the canonical defaults for this example.
            // Restored non-null tags/facts/goal updates are rejected.
            tags = null,
            facts = null,
            goal_updates = null,
        };
    }

    private static ObserveRequest BuildOutcomeObserveRequest(
        string sessionId,
        string operationId,
        long tick,
        AppliedAction applied)
    {
        return new ObserveRequest
        {
            protocol_version = RinClient.ProtocolVersion,
            session_id = sessionId,
            request_id = "reconcile." + operationId,
            event_id = "outcome." + operationId,
            tick = tick,
            // This example owns exactly npc.mira. A persisted or remote actor
            // cannot redirect authoritative outcome memory to another observer.
            observer_ids = new[] { "npc.mira" },
            source = "unity-example",
            kind = "action_outcome",
            summary = "Authoritative outcome: " + applied.outcome,
            quote = null,
            tags = new[] { "outcome-report" },
            importance = 3,
            facts = null,
        };
    }

    private static ObserveRequest BuildFallbackObserveRequest(
        string sessionId,
        string operationId,
        long tick,
        AppliedAction applied)
    {
        return new ObserveRequest
        {
            protocol_version = RinClient.ProtocolVersion,
            session_id = sessionId,
            request_id = "reconcile." + operationId,
            event_id = "fallback." + operationId,
            tick = tick,
            observer_ids = new[] { "npc.mira" },
            source = "unity-example",
            kind = "fallback_action",
            summary = "Local fallback " + applied.actionId + ": " + applied.outcome,
            quote = null,
            tags = new[] { "fallback" },
            importance = 3,
            facts = null,
        };
    }

    private static CreateSessionRequest BuildCreateRequest(string stableRunId)
    {
        return new CreateSessionRequest
        {
            request_id = "create." + stableRunId,
            session_id = "playthrough." + stableRunId,
            binding = new Binding
            {
                game_id = "example-game",
                content_id = "base",
                content_version = "1.0.0",
                content_hash = "example-content-hash",
            },
            seed = 42,
            features = new[] { "outcome-reporting-v1" },
            actors = new[]
            {
                new ActorSeed
                {
                    id = "npc.mira",
                    kind = "npc",
                    display_name = "Mira",
                    traits = new[] { "careful" },
                    goals = new[]
                    {
                        new Goal
                        {
                            id = "goal.connect",
                            description = "Build trust through specific actions.",
                            priority = 4,
                            preferred_actions = new[] { "talk" },
                            progress = 0,
                            target_progress = 3,
                            status = "active",
                        },
                    },
                    think_every_ticks = NpcThinkEveryTicks,
                    enabled = true,
                },
            },
        };
    }

    private static ProposeRequest BuildProposeRequest(
        string sessionId,
        string operationId,
        long tick)
    {
        return new ProposeRequest
        {
            session_id = sessionId,
            request_id = "propose." + operationId,
            actor_id = "npc.mira",
            tick = tick,
            intent = "Choose how to respond to the player.",
            tags = new[] { "conversation", "trust" },
            candidate_actions = new[]
            {
                new ActionSpec
                {
                    id = "talk",
                    kind = "dialogue",
                    description = "Ask one honest question.",
                },
                new ActionSpec
                {
                    id = "wait",
                    kind = "wait",
                    description = "Stay silent for now.",
                },
            },
        };
    }

    private static bool SemanticDtoEquals(object left, object right)
    {
        if (ReferenceEquals(left, right)) return true;
        if (left == null || right == null || left.GetType() != right.GetType())
            return false;
        var type = left.GetType();
        if (type.IsPrimitive || type.IsEnum || type == typeof(string) ||
            type == typeof(decimal))
            return left.Equals(right);
        var leftDictionary = left as IDictionary;
        var rightDictionary = right as IDictionary;
        if (leftDictionary != null || rightDictionary != null)
        {
            if (leftDictionary == null ||
                rightDictionary == null ||
                leftDictionary.Count != rightDictionary.Count)
                return false;
            foreach (DictionaryEntry entry in leftDictionary)
            {
                if (!rightDictionary.Contains(entry.Key) ||
                    !SemanticDtoEquals(entry.Value, rightDictionary[entry.Key]))
                    return false;
            }
            return true;
        }
        var leftList = left as IList;
        var rightList = right as IList;
        if (leftList != null || rightList != null)
        {
            if (leftList == null ||
                rightList == null ||
                leftList.Count != rightList.Count)
                return false;
            for (var index = 0; index < leftList.Count; index++)
            {
                if (!SemanticDtoEquals(leftList[index], rightList[index]))
                    return false;
            }
            return true;
        }
        foreach (var field in type.GetFields(BindingFlags.Instance | BindingFlags.Public))
        {
            if (!SemanticDtoEquals(field.GetValue(left), field.GetValue(right)))
                return false;
        }
        return true;
    }

    private bool PersistNewProposalAttempt(
        string sessionId,
        ProposalAttempt attempt,
        long sequence,
        long authoritativeTick)
    {
        if (!authoritativeStateReady) return false;
        if (attempt == null ||
            attempt.request == null ||
            operationSequence == long.MaxValue ||
            sequence != operationSequence + 1 ||
            authoritativeTick <= lastAuthoritativeTick ||
            !TryParseOperationSequence(attempt.operationId, runId, out var parsedSequence) ||
            parsedSequence != sequence ||
            attempt.sequence != sequence ||
            attempt.request.session_id != sessionId ||
            attempt.request.request_id != "propose." + attempt.operationId ||
            !SemanticDtoEquals(
                attempt.request,
                BuildProposeRequest(sessionId, attempt.operationId, authoritativeTick)) ||
            attempt.fallbackActionId != "wait" ||
            attempt.request.tick != authoritativeTick)
            return false;
        // PRODUCTION PERSISTENCE HOOK: atomically save the complete attempt and
        // consumed game sequence and lastAuthoritativeTick before any online
        // submission or local fallback.
        return true;
    }

    private bool RecordProposalJobId(
        string sessionId,
        string operationId,
        string jobId)
    {
        if (!RinClient.IsProtocolId(jobId) ||
            !proposalAttempts.TryGetValue(sessionId, out var attempt) ||
            attempt.operationId != operationId)
            return false;
        if (attempt.jobId == jobId) return true;
        if (!PersistProposalJobId(sessionId, operationId, jobId)) return false;
        attempt.jobId = jobId;
        return true;
    }

    private bool PersistProposalJobId(
        string sessionId,
        string operationId,
        string jobId)
    {
        // PRODUCTION PERSISTENCE HOOK: durably attach the 202 Job ID to the
        // matching stable attempt before the adapter starts polling it.
        return true;
    }

    private AppliedAction ApplyAndEnqueueAuthoritativeOperation(
        string sessionId,
        string operationId,
        AppliedAction planned,
        PendingReport report,
        long proposalTick)
    {
        if (!authoritativeStateReady) return null;
        if (appliedOperations.TryGetValue(operationId, out var stored))
        {
            // Atomic persistence guarantees its report is still queued until
            // acknowledgement. Never execute the game effect again.
            return stored;
        }
        if (!PersistAuthoritativeTransaction(
            sessionId,
            operationId,
            planned,
            report,
            proposalTick))
            return null;
        return appliedOperations.TryGetValue(operationId, out var applied)
            ? applied
            : null;
    }

    private bool PersistAuthoritativeTransaction(
        string sessionId,
        string operationId,
        AppliedAction planned,
        PendingReport report,
        long proposalTick)
    {
        if (!authoritativeStateReady) return false;
        // PRODUCTION PERSISTENCE HOOK: replace this whole body with one atomic
        // game transaction. The actual Unity game-state effect, applied marker,
        // complete report (including safe fallback), Proposal-attempt deletion,
        // runId, sequence, and last authoritative tick must commit or roll back
        // together.
        if (!proposalAttempts.TryGetValue(sessionId, out var proposalAttempt) ||
            proposalAttempt.operationId != operationId)
            return false;
        if (proposalAttempt.request == null ||
            proposalAttempt.request.tick < 0 ||
            proposalTick < 0)
            return false;
        return RunAuthoritativeGameTransaction(transaction =>
        {
            // Unity's frame counter may reset after a process or scene restart.
            // Preserve the causal floor of the retained request and response.
            var occurrenceTick = Math.Max(
                Math.Max(CaptureAuthoritativeOccurrenceTick(), lastAuthoritativeTick),
                Math.Max(proposalAttempt.request.tick, proposalTick));
            var effectivePlanned = planned;
            if (planned.accepted &&
                occurrenceTick > long.MaxValue - NpcThinkEveryTicks)
            {
                // An accepted Commit schedules npc.mira at
                // tick + think_every_ticks. Reject before applying any effect
                // when that addition cannot fit in int64.
                effectivePlanned = new AppliedAction(
                    planned.actionId,
                    false,
                    "The game rejected the action because the scheduler tick range is exhausted.");
            }
            var persistedReport = report
                .WithAppliedOutcome(effectivePlanned)
                .WithOccurrenceTick(occurrenceTick);
            var previousLastTick = lastAuthoritativeTick;
            lastAuthoritativeTick = occurrenceTick;
            transaction.OnRollback(() => lastAuthoritativeTick = previousLastTick);
            ApplyPlannedGameEffect(effectivePlanned, transaction);
            appliedOperations.Add(operationId, effectivePlanned);
            transaction.OnRollback(() => appliedOperations.Remove(operationId));
            reportOutbox.Add(operationId, persistedReport);
            transaction.OnRollback(() => reportOutbox.Remove(operationId));
            // A succeeded online proposal (or confirmed-safe offline terminal)
            // stops being resumable only in this authoritative transaction.
            proposalAttempts.Remove(sessionId);
            transaction.OnRollback(() => proposalAttempts[sessionId] = proposalAttempt);
            return CommitAuthoritativeGameTransaction(operationId, occurrenceTick);
        });
    }

    private IEnumerator FlushReportOutbox(Action<bool> completed)
    {
        if (!authoritativeStateReady)
        {
            completed(false);
            yield break;
        }
        var operationIds = new List<string>(reportOutbox.Keys);
        operationIds.Sort(StringComparer.Ordinal);
        foreach (var operationId in operationIds)
        {
            var pending = reportOutbox[operationId];
            MutationResult committed = null;
            if (pending.kind == "commit")
            {
                ReportAttempt attempt = null;
                yield return rin.CommitReport(
                    (CommitRequest)pending.request,
                    value => attempt = value);
                if (attempt != null && attempt.ok)
                {
                    committed = attempt.data;
                }
                else
                {
                    var errorCode = attempt != null ? attempt.error_code : "unknown";
                    if (!IsIrrecoverableCommitError(errorCode))
                    {
                        Debug.LogError(
                            "Commit temporarily failed; its exact request remains queued.");
                        completed(false);
                        yield break;
                    }
                    var replacement = pending.AsFallbackObserve();
                    if (!PersistReportConversion(operationId, replacement))
                    {
                        Debug.LogError(
                            "Could not durably convert Commit; original remains queued.");
                        completed(false);
                        yield break;
                    }
                    reportOutbox[operationId] = replacement;
                    pending = replacement;
                    yield return rin.Observe(
                        (ObserveRequest)pending.request,
                        value => committed = value);
                }
            }
            else if (pending.kind == "observe")
                yield return rin.Observe((ObserveRequest)pending.request, value => committed = value);
            else
            {
                Debug.LogError("Unknown authoritative report kind; entry remains queued.");
                completed(false);
                yield break;
            }
            if (committed == null)
            {
                Debug.LogError(
                    "Game action already handled; the same report remains queued for retry.");
                completed(false);
                yield break;
            }
            if (!PersistReportAcknowledgement(operationId))
            {
                Debug.LogError(
                    "Report was acknowledged but durable Outbox deletion failed; retry is safe.");
                completed(false);
                yield break;
            }
            reportOutbox.Remove(operationId);
        }
        completed(true);
    }

    private bool PersistReportAcknowledgement(string operationId)
    {
        // PRODUCTION PERSISTENCE HOOK: durably delete operationId's Outbox row.
        // Only after true may the caller evict its in-memory copy.
        return true;
    }

    private bool PersistReportConversion(string operationId, PendingReport replacement)
    {
        // PRODUCTION PERSISTENCE HOOK: atomically replace the Commit row with
        // replacement before changing the in-memory cache.
        return true;
    }

    private bool CommitAuthoritativeGameTransaction(
        string operationId,
        long authoritativeTick)
    {
        // PRODUCTION PERSISTENCE HOOK: false aborts effect, marker, Outbox,
        // runId, sequence, and lastAuthoritativeTick together.
        return authoritativeTick == lastAuthoritativeTick;
    }

    private bool RunAuthoritativeGameTransaction(Func<GameTransaction, bool> mutate)
    {
        var transaction = new GameTransaction();
        try
        {
            if (mutate(transaction)) return true;
        }
        catch (Exception error)
        {
            Debug.LogError("Authoritative game transaction failed: " + error.Message);
        }
        transaction.Rollback();
        return false;
    }

    private long CaptureAuthoritativeOccurrenceTick()
    {
        // Read the current game clock inside the transaction at actual
        // apply/reject. Production games should inject their persisted
        // simulation clock here.
        return Math.Max(0L, (long)Time.frameCount);
    }

    private bool TryAllocateFreshProposalTick(out long tick)
    {
        tick = 0;
        if (lastAuthoritativeTick == long.MaxValue) return false;
        // Keep a larger live simulation clock; otherwise advance the restored
        // durable high-water by one after a process/scene clock reset.
        tick = Math.Max(
            CaptureAuthoritativeOccurrenceTick(),
            lastAuthoritativeTick + 1);
        return true;
    }

    private static bool ProposalIsFresh(
        ProposalFreshnessResult state,
        ActionProposal proposal,
        ProposeRequest stableRequest)
    {
        if (state == null || proposal == null || stableRequest == null)
            return false;
        var retained = state.proposal;
        ActionSpec stableAction = null;
        if (stableRequest.candidate_actions != null && proposal.action != null)
        {
            foreach (var candidate in stableRequest.candidate_actions)
            {
                if (candidate != null && candidate.id == proposal.action.id)
                {
                    stableAction = candidate;
                    break;
                }
            }
        }
        if (retained == null ||
            string.IsNullOrEmpty(retained.id) ||
            retained.id != proposal.id ||
            retained.status != "pending" ||
            string.IsNullOrEmpty(retained.session_id) ||
            retained.session_id != proposal.session_id ||
            string.IsNullOrEmpty(retained.request_id) ||
            retained.request_id != proposal.request_id ||
            string.IsNullOrEmpty(retained.actor_id) ||
            retained.actor_id != proposal.actor_id ||
            retained.tick < 0 ||
            retained.tick != proposal.tick ||
            retained.action == null ||
            proposal.action == null ||
            string.IsNullOrEmpty(retained.action.id) ||
            retained.action.id != proposal.action.id ||
            string.IsNullOrEmpty(retained.action.kind) ||
            retained.action.kind != proposal.action.kind ||
            !SemanticDtoEquals(retained.action, proposal.action) ||
            stableAction == null ||
            !SemanticDtoEquals(stableAction, proposal.action) ||
            retained.has_unsupported_action_parameters ||
            proposal.has_unsupported_action_parameters ||
            retained.based_on_revision < 0 ||
            retained.based_on_revision != proposal.based_on_revision ||
            retained.based_on_head_hash != proposal.based_on_head_hash ||
            retained.based_on_world_revision < 0 ||
            retained.based_on_world_revision != proposal.based_on_world_revision ||
            retained.created_revision < 0 ||
            retained.created_revision != proposal.created_revision)
            return false;
        return retained.based_on_world_revision > 0
            ? state.world_revision == retained.based_on_world_revision
            : state.revision == retained.created_revision;
    }

    private static bool IsIrrecoverableCommitError(string errorCode)
    {
        return errorCode == "session_not_found" ||
            errorCode == "unknown_proposal" ||
            errorCode == "proposal_resolved" ||
            errorCode == "proposal_canceled" ||
            errorCode == "proposal_stale";
    }

    private AppliedAction PlanActionInGame(ActionSpec action)
    {
        if (action == null || (action.id != "talk" && action.id != "wait"))
            return new AppliedAction(
                action != null ? action.id : "",
                false,
                "The game rejected an action outside its local allowlist.");
        return new AppliedAction(
            action.id,
            true,
            "The game applied the advertised action.");
    }

    private void ApplyPlannedGameEffect(
        AppliedAction planned,
        GameTransaction transaction)
    {
        // Replace with navigation, animation, dialogue, or combat owned by
        // Unity. Register the inverse before mutating; an exception then rolls
        // the effect back with the marker and Outbox.
        if (planned.accepted)
        {
            transaction.OnRollback(
                () => Debug.Log("Roll back game-owned action: " + planned.actionId));
            Debug.Log("Apply game-owned action: " + planned.actionId);
        }
    }

    private enum AuthoritativeStateLoadStatus
    {
        Loaded,
        NotFound,
        Failed,
    }

    private sealed class AuthoritativeStateLoadResult
    {
        public readonly AuthoritativeStateLoadStatus status;
        public readonly AuthoritativeState state;
        public readonly string error;

        private AuthoritativeStateLoadResult(
            AuthoritativeStateLoadStatus status,
            AuthoritativeState state,
            string error)
        {
            this.status = status;
            this.state = state;
            this.error = error;
        }

        public static AuthoritativeStateLoadResult Loaded(AuthoritativeState state)
        {
            return new AuthoritativeStateLoadResult(
                AuthoritativeStateLoadStatus.Loaded,
                state,
                null);
        }

        public static AuthoritativeStateLoadResult NotFound()
        {
            return new AuthoritativeStateLoadResult(
                AuthoritativeStateLoadStatus.NotFound,
                null,
                null);
        }

        public static AuthoritativeStateLoadResult Failed(string error)
        {
            return new AuthoritativeStateLoadResult(
                AuthoritativeStateLoadStatus.Failed,
                null,
                error);
        }
    }

    // These DTOs intentionally avoid Dictionary and polymorphic object fields,
    // so a game can serialize them with JsonUtility or its own save system.
    [Serializable]
    private sealed class AuthoritativeState
    {
        public int schemaVersion;
        public string runId;
        public long operationSequence;
        public long lastAuthoritativeTick;
        public CreateSessionRequest createRequest;
        public ProposalAttemptState[] proposalAttempts;
        public AppliedOperationState[] appliedOperations;
        public PendingReportState[] reportOutbox;
    }

    [Serializable]
    private sealed class ProposalAttemptState
    {
        public string sessionId;
        public string operationId;
        public long sequence;
        public ProposeRequest request;
        public string fallbackActionId;
        public string jobId;
    }

    [Serializable]
    private sealed class AppliedOperationState
    {
        public string operationId;
        public string actionId;
        public bool accepted;
        public string outcome;
    }

    [Serializable]
    private sealed class PendingReportState
    {
        public string operationId;
        public string kind;
        public CommitRequest commit;
        public ObserveRequest observe;
        public ObserveRequest fallback;
    }

    private sealed class GameTransaction
    {
        private readonly List<Action> rollbacks = new List<Action>();

        public void OnRollback(Action rollback)
        {
            if (rollback != null) rollbacks.Add(rollback);
        }

        public void Rollback()
        {
            for (var index = rollbacks.Count - 1; index >= 0; index--)
            {
                try
                {
                    rollbacks[index]();
                }
                catch (Exception error)
                {
                    Debug.LogError("Game rollback failed: " + error.Message);
                }
            }
        }
    }

    private sealed class AppliedAction
    {
        public readonly string actionId;
        public readonly bool accepted;
        public readonly string outcome;

        public AppliedAction(string actionId, bool accepted, string outcome)
        {
            this.actionId = actionId;
            this.accepted = accepted;
            this.outcome = outcome;
        }
    }

    private sealed class ProposalAttempt
    {
        public readonly string operationId;
        public readonly long sequence;
        public readonly ProposeRequest request;
        public readonly string fallbackActionId;
        public string jobId;

        public ProposalAttempt(
            string operationId,
            long sequence,
            ProposeRequest request,
            string fallbackActionId,
            string jobId)
        {
            this.operationId = operationId;
            this.sequence = sequence;
            this.request = request;
            this.fallbackActionId = fallbackActionId;
            this.jobId = jobId;
        }
    }

    private sealed class PendingReport
    {
        public readonly string kind;
        public readonly object request;
        public readonly ObserveRequest fallback;

        private PendingReport(string kind, object request, ObserveRequest fallback)
        {
            this.kind = kind;
            this.request = request;
            this.fallback = fallback;
        }

        public static PendingReport Commit(
            CommitRequest request,
            ObserveRequest fallback)
        {
            return new PendingReport("commit", request, fallback);
        }

        public static PendingReport Observe(ObserveRequest request)
        {
            return new PendingReport("observe", request, null);
        }

        public PendingReport WithAppliedOutcome(AppliedAction applied)
        {
            if (kind == "commit")
            {
                var commit = (CommitRequest)request;
                commit.accepted = applied.accepted;
                commit.outcome = applied.outcome;
                fallback.summary = "Authoritative outcome: " + applied.outcome;
            }
            else
            {
                var observe = (ObserveRequest)request;
                observe.summary =
                    "Local fallback " + applied.actionId + ": " + applied.outcome;
            }
            return this;
        }

        public PendingReport WithOccurrenceTick(long tick)
        {
            if (kind == "commit")
            {
                ((CommitRequest)request).tick = tick;
                fallback.tick = tick;
            }
            else
            {
                ((ObserveRequest)request).tick = tick;
            }
            return this;
        }

        public PendingReport AsFallbackObserve()
        {
            return Observe(fallback);
        }
    }
}

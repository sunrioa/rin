using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using BepInEx;
using BepInEx.Configuration;
using Rin.Client;
using UnityEngine;

namespace RinNpcExample;

[BepInPlugin(PluginGuid, PluginName, PluginVersion)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginGuid = "io.github.sunrioa.rin.npc-example";
    public const string PluginName = "Rin NPC Example";
    public const string PluginVersion = "0.1.0";

    private const string ActorId = "npc.rin.companion";
    private const int MaxProposalPostsPerEntry = 2;
    private static readonly HashSet<string> AllowedActions = new(StringComparer.Ordinal)
    {
        "talk",
        "wait",
        "refuse",
    };
    private static readonly HashSet<string> TerminalCommitErrors = new(StringComparer.Ordinal)
    {
        "session_not_found",
        "unknown_proposal",
        "proposal_resolved",
    };
    private static readonly HashSet<string> AmbiguousProposalErrors = new(StringComparer.Ordinal)
    {
        "job_cancel_unconfirmed",
        "job_outcome_unknown",
        "job_timeout",
        "proposal_outcome_unknown",
    };

    private readonly ConcurrentQueue<Action> mainThread = new();
    private readonly ConcurrentDictionary<string, AppliedAction> appliedOperations = new();
    private readonly ConcurrentDictionary<string, PendingOutcome> outcomeOutbox = new();
    private readonly SemaphoreSlim turnGate = new(1, 1);
    private readonly object sessionLock = new();
    private readonly object persistenceLock = new();
    private RinClient? rin;
    private ConfigEntry<string>? baseUrl;
    private ConfigEntry<bool>? demoHotkey;
    private Task? sessionTask;
    private Dictionary<string, object?>? createSessionRequest;
    private string sessionId = string.Empty;
    private long sequence;
    private ProposalAttempt? proposalAttempt;

    public event Action<string, string>? NpcActionReady;

    private void Awake()
    {
        baseUrl = Config.Bind(
            "Connection",
            "BaseUrl",
            RinClient.DefaultBaseUrl,
            "Rin origin. Remote origins require HTTPS and RIN_TOKEN in the process environment.");
        demoHotkey = Config.Bind(
            "Example",
            "EnableF8Demo",
            true,
            "Press F8 to request one example NPC turn.");
        sessionId = "bepinex." + Guid.NewGuid().ToString("N");

        // This complete Create request is retained for the lifetime of the
        // session. Ambiguous retries reuse the same request_id, seed, binding,
        // actors, and feature set.
        createSessionRequest = CreateSessionPayload(
            sessionId,
            Application.productName,
            DateTimeOffset.UtcNow.ToUnixTimeSeconds());

        try
        {
            rin = new RinClient(new RinClientOptions
            {
                BaseUrl = baseUrl.Value,
                Token = Environment.GetEnvironmentVariable("RIN_TOKEN") ?? string.Empty,
            });
            Logger.LogInfo("Rin NPC example loaded. No network request runs until an interaction is triggered.");
        }
        catch (RinException exception)
        {
            Logger.LogError("Rin configuration rejected: " + exception.Code);
        }
    }

    private void Update()
    {
        for (var count = 0; count < 64 && mainThread.TryDequeue(out var action); count++)
        {
            action();
        }
        if (rin is not null && demoHotkey?.Value == true && Input.GetKeyDown(KeyCode.F8))
        {
            RequestNpcTurn(
                "The player requested guidance from the companion.",
                Time.frameCount);
        }
    }

    private void OnDestroy()
    {
        rin?.Dispose();
        turnGate.Dispose();
    }

    public void RequestNpcTurn(string observation, long gameTick)
    {
        if (rin is null) return;
        _ = RunNpcTurnAsync(observation, gameTick);
    }

    private async Task RunNpcTurnAsync(string observation, long observedGameTick)
    {
        if (rin is null) return;
        await turnGate.WaitAsync().ConfigureAwait(false);
        try
        {
            var retainedAttempt = RetainedProposalAttempt();
            try
            {
                await EnsureSessionAsync().ConfigureAwait(false);
            }
            catch (Exception exception)
            {
                InvalidateSessionIfNotFound(exception);
                // Offline fallback is authored game content, and is allowed
                // only before Rin has supplied a proposal. If an earlier
                // report or unresolved submission is pending, no new turn may
                // start and the exact proposal identity remains retained.
                if (retainedAttempt is not null || !outcomeOutbox.IsEmpty)
                {
                    EnqueueIntegrationFailure(ExceptionCode(exception), actionHandled: true);
                    return;
                }
                var offlineTurn = Interlocked.Increment(ref sequence);
                await ApplyOfflineFallbackOnMainThreadAsync(
                    sessionId + ".offline." + offlineTurn).ConfigureAwait(false);
                EnqueueLog("Rin was unavailable; the authored offline fallback was applied and queued.");
                return;
            }

            // A new authoritative entry retries every retained report first.
            // Temporary failure preserves it and prevents this turn.
            await FlushOutcomeOutboxAsync(sessionId).ConfigureAwait(false);
            var attempt = retainedAttempt
                ?? RetainNewProposalAttempt(observation, observedGameTick);

            // Observe is itself idempotent. Replaying its exact retained
            // payload closes an ambiguous Observe before resuming the same
            // Propose request and never consumes a new sequence number.
            await rin.ObserveAsync(attempt.ObserveRequest).ConfigureAwait(false);
            var resolution = await ResolveProposalAttemptAsync(attempt).ConfigureAwait(false);
            if (resolution.UseAuthoredFallback)
            {
                if (string.Equals(
                    resolution.Reason,
                    "session_not_found",
                    StringComparison.Ordinal))
                {
                    lock (sessionLock) sessionTask = null;
                }
                await ApplyOfflineFallbackOnMainThreadAsync(
                    attempt.OperationId,
                    attempt).ConfigureAwait(false);
                await ReportOutcomeAsync(attempt.OperationId).ConfigureAwait(false);
                EnqueueLog(
                    "Rin confirmed a terminal proposal failure (" + resolution.Reason
                    + "); the authored fallback outcome was acknowledged.");
                return;
            }
            var proposal = resolution.Proposal;

            // The game is the world authority. Re-read Rin immediately before
            // apply. A temporary State failure fails closed and is never
            // reinterpreted as permission to run the offline fallback.
            FreshnessDecision freshness;
            try
            {
                var state = await rin.StateAsync(new Dictionary<string, object?>
                {
                    ["protocol_version"] = RinClient.ProtocolVersion,
                    ["session_id"] = sessionId,
                }).ConfigureAwait(false);
                freshness = ProposalFreshness(state, proposal);
            }
            catch (RinException exception)
            {
                InvalidateSessionIfNotFound(exception);
                // No authored fallback is allowed after an online proposal.
                // Record an authoritative rejection instead; if reporting is
                // also unavailable its complete Commit remains in the Outbox.
                freshness = FreshnessDecision.Unavailable;
            }
            await ApplyAndEnqueueOnMainThreadAsync(
                attempt.OperationId,
                sessionId,
                proposal,
                freshness,
                attempt).ConfigureAwait(false);

            await ReportOutcomeAsync(attempt.OperationId).ConfigureAwait(false);
            EnqueueLog("Rin outcome acknowledged.");
        }
        catch (Exception exception)
        {
            InvalidateSessionIfNotFound(exception);
            EnqueueIntegrationFailure(
                ExceptionCode(exception),
                actionHandled: !outcomeOutbox.IsEmpty
                    || RetainedProposalAttempt() is not null);
        }
        finally
        {
            turnGate.Release();
        }
    }

    private ProposalAttempt RetainNewProposalAttempt(
        string observation,
        long observedGameTick)
    {
        lock (persistenceLock)
        {
            if (proposalAttempt is not null) return proposalAttempt;
            var turn = checked(sequence + 1);
            var operationId = sessionId + "." + turn;
            var requestId = "propose." + operationId;
            var proposeTick = checked(observedGameTick + 1);
            var retained = new ProposalAttempt(
                sessionId,
                operationId,
                turn,
                requestId,
                proposeTick,
                new Dictionary<string, object?>
                {
                    ["protocol_version"] = RinClient.ProtocolVersion,
                    ["session_id"] = sessionId,
                    ["request_id"] = "observe." + operationId,
                    ["event_id"] = "event." + operationId,
                    ["tick"] = observedGameTick,
                    ["observer_ids"] = new[] { ActorId },
                    ["source"] = "bepinex-example",
                    ["kind"] = "dialogue",
                    ["summary"] = observation,
                    ["tags"] = new[] { "conversation", "player-request" },
                    ["importance"] = 3,
                },
                new Dictionary<string, object?>
                {
                    ["protocol_version"] = RinClient.ProtocolVersion,
                    ["session_id"] = sessionId,
                    ["request_id"] = requestId,
                    ["actor_id"] = ActorId,
                    ["tick"] = proposeTick,
                    ["intent"] = "Choose one bounded response to the player.",
                    ["tags"] = new[] { "conversation" },
                    ["candidate_actions"] = new object[]
                    {
                        ActionSpec("talk", "dialogue", "offer one concrete hint"),
                        ActionSpec("wait", "wait", "ask the player to observe first"),
                        ActionSpec("refuse", "refuse", "decline an unsafe request"),
                    },
                });

            // PRODUCTION PERSISTENCE HOOK: durably store the complete Observe
            // and Propose requests, operation ID, sequence, and empty job ID
            // before the first POST. All later entries restore this object.
            sequence = turn;
            proposalAttempt = retained;
            return retained;
        }
    }

    private ProposalAttempt? RetainedProposalAttempt()
    {
        lock (persistenceLock) return proposalAttempt;
    }

    private async Task<ProposalResolution> ResolveProposalAttemptAsync(
        ProposalAttempt attempt)
    {
        if (rin is null) throw new InvalidOperationException("Rin is not configured");
        var remainingPosts = MaxProposalPostsPerEntry;
        while (true)
        {
            var jobId = attempt.JobId;
            if (string.IsNullOrEmpty(jobId))
            {
                if (remainingPosts <= 0)
                    throw UnknownProposalOutcome();
                var queued = await rin.SubmitProposalJobAsync(
                    attempt.ProposeRequest).ConfigureAwait(false);
                jobId = RequiredString(queued, "job_id");
                PersistProposalJobId(attempt, jobId);
                remainingPosts--;
            }

            try
            {
                // Validate the retained Job envelope before delegating polling
                // to the SDK. Job identity is immutable for this handle, so a
                // later terminal exception belongs to this exact attempt.
                var currentJob = await rin.GetProposalJobAsync(jobId).ConfigureAwait(false);
                ValidateJobIdentity(attempt, jobId, currentJob);
                var currentStatus = RequiredString(currentJob, "status");
                if (string.Equals(currentStatus, "succeeded", StringComparison.Ordinal))
                    return ProposalResolution.FromProposal(
                        ValidateProposalIdentity(attempt, jobId, currentJob));
                if (currentStatus is "failed" or "stale" or "canceled")
                    throw TerminalJobError(currentJob, currentStatus);
                if (currentStatus is not ("queued" or "running"))
                    throw new RinProtocolException(
                        "invalid_job",
                        "Rin returned an unknown proposal Job status");

                var job = await rin.WaitForProposalAsync(jobId).ConfigureAwait(false);
                return ProposalResolution.FromProposal(
                    ValidateProposalIdentity(attempt, jobId, job));
            }
            catch (RinApiException exception)
                when (ShouldRepostProposal(exception.Code))
            {
                // A missing Job or a terminal proposal_outcome_unknown is not
                // permission to fall back. Forget only the lookup handle, then
                // re-POST the byte-for-byte semantic request with the same
                // request_id. The per-entry bound prevents a hot retry loop.
                PersistProposalJobId(attempt, string.Empty);
                if (remainingPosts <= 0) throw UnknownProposalOutcome(exception);
            }
            catch (RinApiException exception)
                when (IsConfirmedSafeTerminal(exception))
            {
                return ProposalResolution.AuthoredFallback(exception.Code);
            }
        }
    }

    private void PersistProposalJobId(ProposalAttempt attempt, string jobId)
    {
        lock (persistenceLock)
        {
            if (!ReferenceEquals(proposalAttempt, attempt))
                throw new InvalidOperationException("Proposal attempt changed before its job ID was persisted");

            // PRODUCTION PERSISTENCE HOOK: atomically update the optional job
            // ID immediately after a 202 response and before the first GET.
            attempt.JobId = jobId;
        }
    }

    private static bool ShouldRepostProposal(string code) =>
        string.Equals(code, "job_not_found", StringComparison.Ordinal)
        || string.Equals(code, "proposal_outcome_unknown", StringComparison.Ordinal);

    private static bool IsConfirmedSafeTerminal(RinApiException exception) =>
        exception.Status == 0
        && !AmbiguousProposalErrors.Contains(exception.Code);

    private static RinApiException UnknownProposalOutcome(Exception? inner = null) =>
        new(
            "proposal_outcome_unknown",
            inner is null
                ? "Proposal outcome remains unknown after bounded same-request retries"
                : "Proposal outcome remains unknown after bounded same-request retries: "
                    + inner.Message);

    private static JsonElement ValidateProposalIdentity(
        ProposalAttempt attempt,
        string expectedJobId,
        JsonElement job)
    {
        ValidateJobIdentity(attempt, expectedJobId, job);

        var proposal = RequiredObject(job, "proposal");
        var proposalId = RequiredString(proposal, "id");
        if (string.IsNullOrWhiteSpace(proposalId)
            || !string.Equals(
                RequiredString(proposal, "session_id"),
                attempt.SessionId,
                StringComparison.Ordinal)
            || !string.Equals(
                RequiredString(proposal, "request_id"),
                attempt.RequestId,
                StringComparison.Ordinal)
            || !string.Equals(
                RequiredString(proposal, "actor_id"),
                ActorId,
                StringComparison.Ordinal)
            || OptionalInt64(proposal, "tick", long.MinValue)
                != attempt.ProposeTick)
        {
            throw new RinProtocolException(
                "proposal_identity_mismatch",
                "Rin returned a Proposal for a different retained proposal attempt");
        }
        return proposal;
    }

    private static void ValidateJobIdentity(
        ProposalAttempt attempt,
        string expectedJobId,
        JsonElement job)
    {
        if (!string.Equals(
                RequiredString(job, "job_id"),
                expectedJobId,
                StringComparison.Ordinal)
            || !string.Equals(
                RequiredString(job, "session_id"),
                attempt.SessionId,
                StringComparison.Ordinal)
            || !string.Equals(
                RequiredString(job, "request_id"),
                attempt.RequestId,
                StringComparison.Ordinal))
        {
            throw new RinProtocolException(
                "proposal_identity_mismatch",
                "Rin returned a Job for a different retained proposal attempt");
        }
    }

    private static RinApiException TerminalJobError(
        JsonElement job,
        string status)
    {
        var code = "job_" + status;
        var message = "Rin proposal Job ended as " + status;
        if (job.TryGetProperty("error", out var error)
            && error.ValueKind == JsonValueKind.Object)
        {
            if (error.TryGetProperty("code", out var errorCode)
                && errorCode.ValueKind == JsonValueKind.String
                && !string.IsNullOrWhiteSpace(errorCode.GetString()))
                code = errorCode.GetString() ?? code;
            if (error.TryGetProperty("message", out var errorMessage)
                && errorMessage.ValueKind == JsonValueKind.String
                && !string.IsNullOrWhiteSpace(errorMessage.GetString()))
                message = errorMessage.GetString() ?? message;
        }
        return new RinApiException(code, message);
    }

    private void InvalidateSessionIfNotFound(Exception exception)
    {
        for (Exception? current = exception; current is not null; current = current.InnerException)
        {
            if (current is RinException rinException
                && string.Equals(
                    rinException.Code,
                    "session_not_found",
                    StringComparison.Ordinal))
            {
                lock (sessionLock) sessionTask = null;
                return;
            }
        }
    }

    private async Task FlushOutcomeOutboxAsync(string currentSessionId)
    {
        foreach (var entry in outcomeOutbox)
        {
            if (entry.Value.SessionId == currentSessionId)
            {
                await ReportOutcomeAsync(entry.Key).ConfigureAwait(false);
            }
        }
    }

    private async Task ReportOutcomeAsync(string operationId)
    {
        if (rin is null) throw new InvalidOperationException("Rin is not configured");
        if (!outcomeOutbox.TryGetValue(operationId, out var pending)) return;

        if (pending.Kind == OutcomeKind.Observe)
        {
            await rin.ObserveAsync(pending.Request).ConfigureAwait(false);
            AcknowledgeOutcome(operationId, pending);
            return;
        }

        try
        {
            await rin.CommitAsync(pending.Request).ConfigureAwait(false);
            AcknowledgeOutcome(operationId, pending);
        }
        catch (RinException exception) when (TerminalCommitErrors.Contains(exception.Code))
        {
            var converted = pending.AsDegradedObserve();
            if (!PersistOutboxConversion(operationId, pending, converted))
                throw new InvalidOperationException("Outbox conversion was not persisted");
            if (!outcomeOutbox.TryUpdate(operationId, converted, pending))
                throw new InvalidOperationException("Outbox changed during conversion");

            if (string.Equals(exception.Code, "session_not_found", StringComparison.Ordinal))
            {
                lock (sessionLock) sessionTask = null;
                // The next entry recreates the exact session and flushes the
                // converted Observe before beginning another turn.
                throw;
            }

            await rin.ObserveAsync(converted.Request).ConfigureAwait(false);
            AcknowledgeOutcome(operationId, converted);
        }
    }

    private void AcknowledgeOutcome(string operationId, PendingOutcome pending)
    {
        // Durable ACK/delete succeeds before in-memory eviction.
        if (!PersistOutboxAcknowledgement(operationId, pending))
            throw new InvalidOperationException("Outbox acknowledgement was not persisted");
        if (!outcomeOutbox.TryRemove(operationId, out var removed)
            || !ReferenceEquals(removed, pending))
            throw new InvalidOperationException("Outbox changed during acknowledgement");
    }

    private Task EnsureSessionAsync()
    {
        lock (sessionLock)
        {
            return sessionTask ??= CreateSessionAsync();
        }
    }

    private async Task CreateSessionAsync()
    {
        if (rin is null || createSessionRequest is null)
            throw new InvalidOperationException("Rin is not configured");
        try
        {
            // The retained object is deliberately reused on every retry.
            await rin.CreateSessionAsync(createSessionRequest).ConfigureAwait(false);
        }
        catch
        {
            lock (sessionLock) sessionTask = null;
            throw;
        }
    }

    private static Dictionary<string, object?> CreateSessionPayload(
        string currentSessionId,
        string currentGameId,
        long seed) => new()
    {
        ["protocol_version"] = RinClient.ProtocolVersion,
        ["request_id"] = "create." + currentSessionId,
        ["session_id"] = currentSessionId,
        ["binding"] = new Dictionary<string, object?>
        {
            ["game_id"] = currentGameId,
            ["content_id"] = "rin-bepinex-example",
            ["content_version"] = PluginVersion,
            ["content_hash"] = "sha256:" + new string('0', 64),
        },
        ["seed"] = seed,
        ["features"] = new[] { "outcome-reporting-v1" },
        ["actors"] = new object[]
        {
            new Dictionary<string, object?>
            {
                ["id"] = ActorId,
                ["kind"] = "npc",
                ["display_name"] = "Rin Companion",
                ["traits"] = new[] { "observant", "careful" },
                ["boundaries"] = new object[]
                {
                    new Dictionary<string, object?>
                    {
                        ["id"] = "boundary.no-cheats",
                        ["description"] = "Never suggest cheats or bypassing game rules.",
                        ["trigger_tags"] = new[] { "unsafe" },
                        ["response"] = "refuse",
                    },
                },
                ["goals"] = new object[]
                {
                    new Dictionary<string, object?>
                    {
                        ["id"] = "goal.help-player",
                        ["description"] = "Help the player make one informed choice.",
                        ["priority"] = 4,
                        ["preferred_actions"] = new[] { "talk" },
                        ["progress"] = 0,
                        ["target_progress"] = 3,
                        ["status"] = "active",
                    },
                },
                ["think_every_ticks"] = 20,
                ["enabled"] = true,
            },
        },
    };

    private Task<AppliedAction> ApplyAndEnqueueOnMainThreadAsync(
        string operationId,
        string currentSessionId,
        JsonElement proposal,
        FreshnessDecision freshness,
        ProposalAttempt completedAttempt)
    {
        if (appliedOperations.TryGetValue(operationId, out var stored))
            return Task.FromResult(stored);

        var completion = new TaskCompletionSource<AppliedAction>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        mainThread.Enqueue(() =>
        {
            try
            {
                if (appliedOperations.TryGetValue(operationId, out var existing))
                {
                    completion.SetResult(existing);
                    return;
                }
                var action = RequiredObject(proposal, "action");
                var actionId = RequiredString(action, "id");
                AppliedAction planned;
                Action applyGameState;
                if (freshness == FreshnessDecision.Unavailable)
                {
                    planned = new AppliedAction(
                        false,
                        "The game rejected the proposal because freshness could not be verified.");
                    applyGameState = () => { };
                }
                else if (freshness == FreshnessDecision.Stale)
                {
                    planned = new AppliedAction(
                        false,
                        "The game rejected a stale proposal before applying it.");
                    applyGameState = () => { };
                }
                else if (!AllowedActions.Contains(actionId))
                {
                    planned = new AppliedAction(
                        false,
                        "The game rejected an action outside its allowlist.");
                    applyGameState = () => { };
                }
                else
                {
                    var line = actionId switch
                    {
                        "talk" => "Companion: Check your resources before choosing the next route.",
                        "wait" => "Companion: Let us observe one more cycle before acting.",
                        "refuse" => "Companion: I cannot help with an action that breaks the game rules.",
                        _ => throw new InvalidOperationException("allowlist changed during apply"),
                    };
                    planned = new AppliedAction(true, line);
                    applyGameState = () =>
                    {
                        // Subscriber exceptions are part of the fallible game
                        // transaction and must not leave an accepted marker.
                        NpcActionReady?.Invoke(actionId, line);
                        Logger.LogMessage(line);
                    };
                }

                // Outcome time cannot precede either the retained request or
                // the returned Proposal, even when both happen in one frame.
                var occurrenceTick = Math.Max(
                    (long)Time.frameCount,
                    Math.Max(
                        completedAttempt.ProposeTick,
                        OptionalInt64(
                            proposal,
                            "tick",
                            completedAttempt.ProposeTick)));
                var pending = CommitPending(
                    currentSessionId,
                    operationId,
                    RequiredString(proposal, "id"),
                    occurrenceTick,
                    planned);
                if (!PersistAuthoritativeTransaction(
                    operationId,
                    planned,
                    pending,
                    completedAttempt,
                    applyGameState))
                    throw new InvalidOperationException("Authoritative game transaction was not persisted");
                completion.SetResult(planned);
            }
            catch (Exception exception)
            {
                completion.SetException(exception);
            }
        });
        return completion.Task;
    }

    private Task ApplyOfflineFallbackOnMainThreadAsync(
        string operationId,
        ProposalAttempt? completedAttempt = null)
    {
        var completion = new TaskCompletionSource<object?>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        mainThread.Enqueue(() =>
        {
            try
            {
                const string line =
                    "Companion (offline): Stay safe, preserve resources, and observe before acting.";
                var applied = new AppliedAction(true, line);
                var occurrenceTick = completedAttempt is null
                    ? (long)Time.frameCount
                    : Math.Max(
                        (long)Time.frameCount,
                        completedAttempt.ProposeTick);
                var pending = ObservePending(
                    sessionId, operationId, occurrenceTick, applied);
                Action effect = () =>
                {
                    NpcActionReady?.Invoke("wait", line);
                    Logger.LogMessage(line);
                };
                if (!PersistAuthoritativeTransaction(
                    operationId,
                    applied,
                    pending,
                    completedAttempt,
                    effect))
                    throw new InvalidOperationException("Offline game transaction was not persisted");
                completion.SetResult(null);
            }
            catch (Exception exception)
            {
                completion.SetException(exception);
            }
        });
        return completion.Task;
    }

    private static PendingOutcome CommitPending(
        string currentSessionId,
        string operationId,
        string proposalId,
        long occurrenceTick,
        AppliedAction applied)
    {
        var commit = new Dictionary<string, object?>
        {
            ["protocol_version"] = RinClient.ProtocolVersion,
            ["session_id"] = currentSessionId,
            ["request_id"] = "commit." + operationId,
            ["proposal_id"] = proposalId,
            ["event_id"] = "outcome." + operationId,
            ["tick"] = occurrenceTick,
            ["accepted"] = applied.Accepted,
            ["outcome"] = applied.Outcome,
            ["tags"] = new[] { "bepinex-example", "conversation" },
        };
        return new PendingOutcome(
            currentSessionId,
            OutcomeKind.Commit,
            commit,
            SafeObserveRequest(currentSessionId, operationId, occurrenceTick, applied));
    }

    private static PendingOutcome ObservePending(
        string currentSessionId,
        string operationId,
        long occurrenceTick,
        AppliedAction applied)
    {
        var observe = SafeObserveRequest(
            currentSessionId, operationId, occurrenceTick, applied);
        return new PendingOutcome(
            currentSessionId,
            OutcomeKind.Observe,
            observe,
            observe);
    }

    private static Dictionary<string, object?> SafeObserveRequest(
        string currentSessionId,
        string operationId,
        long occurrenceTick,
        AppliedAction applied) => new()
    {
        // Degraded reporting is memory plus one absolute fact; it never
        // duplicates relative goal/progress deltas.
        ["protocol_version"] = RinClient.ProtocolVersion,
        ["session_id"] = currentSessionId,
        ["request_id"] = "fallback.observe." + operationId,
        // Commit and degraded Observe describe one occurrence and therefore
        // deliberately share the same idempotency event ID.
        ["event_id"] = "outcome." + operationId,
        ["tick"] = occurrenceTick,
        ["observer_ids"] = new[] { ActorId },
        ["source"] = "bepinex-example",
        ["kind"] = "action_outcome",
        ["summary"] = applied.Outcome,
        ["tags"] = new[] { "outcome", "degraded-report" },
        ["importance"] = 3,
        ["facts"] = new object[]
        {
            new Dictionary<string, object?>
            {
                ["subject_id"] = ActorId,
                ["predicate"] = "last_action_outcome",
                ["object"] = applied.Accepted ? "accepted" : "rejected",
                ["visibility"] = new[] { ActorId },
                ["confidence"] = 100,
            },
        },
    };

    private bool PersistAuthoritativeTransaction(
        string operationId,
        AppliedAction result,
        PendingOutcome pending,
        ProposalAttempt? completedAttempt,
        Action applyGameState)
    {
        // PRODUCTION PERSISTENCE HOOK: replace this body with one fallible,
        // atomic game-save transaction. The game mutation, applied marker,
        // complete Commit plus degraded-Observe Outbox entry, exact Create
        // request, sequence, and deletion of the completed Proposal attempt
        // must commit or roll back together. This demo removes its
        // marker/outbox and retains the attempt when a subscriber throws, but
        // only a real game transaction can reverse a subscriber's partial
        // world mutation.
        lock (persistenceLock)
        {
            if (appliedOperations.ContainsKey(operationId)) return true;
            if (completedAttempt is not null
                && !ReferenceEquals(proposalAttempt, completedAttempt))
                return false;
            appliedOperations[operationId] = result;
            outcomeOutbox[operationId] = pending;
            try
            {
                applyGameState();
                if (completedAttempt is not null) proposalAttempt = null;
                return true;
            }
            catch
            {
                if (outcomeOutbox.TryGetValue(operationId, out var storedPending)
                    && ReferenceEquals(storedPending, pending))
                    outcomeOutbox.TryRemove(operationId, out _);
                if (appliedOperations.TryGetValue(operationId, out var storedResult)
                    && ReferenceEquals(storedResult, result))
                    appliedOperations.TryRemove(operationId, out _);
                throw;
            }
        }
    }

    private bool PersistOutboxConversion(
        string operationId,
        PendingOutcome original,
        PendingOutcome converted)
    {
        // PRODUCTION PERSISTENCE HOOK: atomically replace only an explicitly
        // unrecoverable Commit with its pre-recorded Observe; return false on
        // save failure so the exact Commit remains.
        return outcomeOutbox.TryGetValue(operationId, out var stored)
            && ReferenceEquals(stored, original)
            && converted.Kind == OutcomeKind.Observe;
    }

    private bool PersistOutboxAcknowledgement(
        string operationId,
        PendingOutcome pending)
    {
        // PRODUCTION PERSISTENCE HOOK: durably delete the acknowledged entry,
        // returning false on save failure. In-memory eviction happens later.
        return outcomeOutbox.TryGetValue(operationId, out var stored)
            && ReferenceEquals(stored, pending);
    }

    private static FreshnessDecision ProposalFreshness(
        JsonElement state,
        JsonElement proposal)
    {
        var proposalId = RequiredString(proposal, "id");
        if (!state.TryGetProperty("proposals", out var proposals)
            || proposals.ValueKind != JsonValueKind.Object
            || !proposals.TryGetProperty(proposalId, out var retained)
            || retained.ValueKind != JsonValueKind.Object
            || !retained.TryGetProperty("status", out var status)
            || status.ValueKind != JsonValueKind.String
            || !string.Equals(status.GetString(), "pending", StringComparison.Ordinal))
            return FreshnessDecision.Stale;

        var basedOnWorld = OptionalInt64(proposal, "based_on_world_revision", 0);
        if (basedOnWorld > 0)
            return OptionalInt64(state, "world_revision", -1) == basedOnWorld
                ? FreshnessDecision.Fresh
                : FreshnessDecision.Stale;
        return OptionalInt64(state, "revision", -1)
            == OptionalInt64(proposal, "created_revision", -2)
                ? FreshnessDecision.Fresh
                : FreshnessDecision.Stale;
    }

    private void EnqueueIntegrationFailure(string code, bool actionHandled)
    {
        if (actionHandled)
        {
            EnqueueLog(
                "A handled game action remains durably queued; no new turn may start (" + code + ").",
                error: true);
            return;
        }
        EnqueueLog(
            "Rin integration failed before a game action was applied (" + code + ").",
            error: true);
    }

    private void EnqueueLog(string message, bool error = false)
    {
        mainThread.Enqueue(() =>
        {
            if (error) Logger.LogError(message); else Logger.LogInfo(message);
        });
    }

    private static Dictionary<string, object?> ActionSpec(
        string id,
        string kind,
        string description) => new()
    {
        ["id"] = id,
        ["kind"] = kind,
        ["description"] = description,
    };

    private static JsonElement RequiredObject(JsonElement parent, string name)
    {
        if (!parent.TryGetProperty(name, out var value)
            || value.ValueKind != JsonValueKind.Object)
            throw new RinProtocolException(
                "invalid_response", "Rin response is missing " + name);
        return value;
    }

    private static string RequiredString(JsonElement parent, string name)
    {
        if (!parent.TryGetProperty(name, out var value)
            || value.ValueKind != JsonValueKind.String)
            throw new RinProtocolException(
                "invalid_response", "Rin response is missing " + name);
        return value.GetString() ?? string.Empty;
    }

    private static long OptionalInt64(
        JsonElement parent,
        string name,
        long fallback)
    {
        return parent.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.Number
            && value.TryGetInt64(out var number)
                ? number
                : fallback;
    }

    private static string ExceptionCode(Exception exception)
    {
        return exception is RinException rinException
            ? rinException.Code
            : "integration_failed";
    }

    private enum OutcomeKind
    {
        Commit,
        Observe,
    }

    private enum FreshnessDecision
    {
        Fresh,
        Stale,
        Unavailable,
    }

    private sealed class AppliedAction
    {
        public AppliedAction(bool accepted, string outcome)
        {
            Accepted = accepted;
            Outcome = outcome;
        }

        public bool Accepted { get; }
        public string Outcome { get; }
    }

    private sealed class ProposalAttempt
    {
        public ProposalAttempt(
            string currentSessionId,
            string operationId,
            long currentSequence,
            string requestId,
            long proposeTick,
            Dictionary<string, object?> observeRequest,
            Dictionary<string, object?> proposeRequest)
        {
            SessionId = currentSessionId;
            OperationId = operationId;
            Sequence = currentSequence;
            RequestId = requestId;
            ProposeTick = proposeTick;
            ObserveRequest = observeRequest;
            ProposeRequest = proposeRequest;
        }

        public string SessionId { get; }
        public string OperationId { get; }
        public long Sequence { get; }
        public string RequestId { get; }
        public long ProposeTick { get; }
        public Dictionary<string, object?> ObserveRequest { get; }
        public Dictionary<string, object?> ProposeRequest { get; }
        public string JobId { get; set; } = string.Empty;
    }

    private sealed class ProposalResolution
    {
        private ProposalResolution(
            JsonElement proposal,
            bool useAuthoredFallback,
            string reason)
        {
            Proposal = proposal;
            UseAuthoredFallback = useAuthoredFallback;
            Reason = reason;
        }

        public JsonElement Proposal { get; }
        public bool UseAuthoredFallback { get; }
        public string Reason { get; }

        public static ProposalResolution FromProposal(JsonElement proposal) =>
            new(proposal, false, string.Empty);

        public static ProposalResolution AuthoredFallback(string reason) =>
            new(default, true, reason);
    }

    private sealed class PendingOutcome
    {
        public PendingOutcome(
            string currentSessionId,
            OutcomeKind kind,
            Dictionary<string, object?> request,
            Dictionary<string, object?> degradedObserveRequest)
        {
            SessionId = currentSessionId;
            Kind = kind;
            Request = request;
            DegradedObserveRequest = degradedObserveRequest;
        }

        public string SessionId { get; }
        public OutcomeKind Kind { get; }
        public Dictionary<string, object?> Request { get; }
        public Dictionary<string, object?> DegradedObserveRequest { get; }

        public PendingOutcome AsDegradedObserve() => new(
            SessionId,
            OutcomeKind.Observe,
            DegradedObserveRequest,
            DegradedObserveRequest);
    }
}

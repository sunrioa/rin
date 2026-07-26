using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Threading;
using UnityEngine;
using UnityEngine.SceneManagement;

[Serializable]
public sealed class RinActionOfferTemplate
{
    public string offer_id;
    public CapabilityRef capability;
    public string descriptor_digest;
    public string description;
    public string arguments_json = "{}";
    public HostRef[] targets = new HostRef[0];
}

[Serializable]
public sealed class RinTurnInput
{
    public string actor_id;
    public long tick;
    public long observation_seq;
    public string clock = "step";
    public long opened_at;
    public long deadline;
    public string intent;
    public string source = "game";
    public string observation_kind = "world.event";
    public string observation_summary;
    public int importance = 1;
    public RinActionOfferTemplate[] offers;
}

[Serializable]
public sealed class RinHostActionResult
{
    public bool accepted;
    public string status = "succeeded";
    public string summary;
    public string code;
    public long world_seq;
    public long occurred_at;
    public HostRef[] evidence = new HostRef[0];

    public static RinHostActionResult Rejected(string summary)
    {
        return new RinHostActionResult
        {
            accepted = false,
            status = "rejected",
            summary = summary,
        };
    }
}

// Restartable advisory/idempotent workflow. The complete request is durable
// before network I/O; applied markers and exact action reports survive restart.
public sealed class RinUnityWorkflow : MonoBehaviour
{
    private const int StateSchemaVersion = 3;
    private const int MaxAppliedMarkers = 256;
    private const int MaxOutboxEntries = 64;
    private const long MaxJsonInteger = 9007199254740991L;
    private static RinUnityWorkflow authorityOwner;

    [SerializeField] private RinClient rin = null;
    [SerializeField] private MonoBehaviour hostComponent = null;
    [SerializeField] private string gameId = "example.unity";
    [SerializeField] private string contentId = "base";
    [SerializeField] private string contentVersion = "1";
    [SerializeField] private string contentHash =
        "0000000000000000000000000000000000000000000000000000000000000000";
    [SerializeField] private string worldId = "unity.world";
    [SerializeField] private string actorId = "npc.guide";
    [SerializeField] private string actorDisplayName = "Guide";

    private IRinUnityHost host;
    private string runId;
    private long operationSequence;
    private long hostEpoch;
    private long worldEpoch;
    private long timelineEpoch;
    private long observationSequence;
    private long lastAuthoritativeTick;
    private PendingTurnState pendingTurn;
    private readonly Dictionary<string, AppliedMarker> applied =
        new Dictionary<string, AppliedMarker>();
    private readonly List<ReportOutboxEntry> reportOutbox =
        new List<ReportOutboxEntry>();
    private readonly Queue<Action> authorityQueue = new Queue<Action>();
    private readonly RinUnityActionGate actionGate = new RinUnityActionGate();
    private ActiveRunState activeRun;
    private bool authoritativeStateReady;
    private bool turnRunning;
    private string statePath;
    private int authorityThread;
    private int activeScene;
    private RinUnityStateFile stateFile;

    private void Awake()
    {
        if (authorityOwner != null &&
            !ReferenceEquals(authorityOwner, this))
        {
            Debug.LogError("Only one RinUnityWorkflow may own a save slot.");
            Destroy(gameObject);
            return;
        }
        authorityOwner = this;
        authorityThread = Thread.CurrentThread.ManagedThreadId;
        statePath = Path.Combine(
            Application.persistentDataPath,
            "rin",
            "default.json");
        stateFile = new RinUnityStateFile(statePath);
        host = hostComponent as IRinUnityHost;
        if (!ValidConfiguration())
        {
            authorityOwner = null;
            Debug.LogError("Rin Unity Host configuration is invalid.");
            return;
        }
        authoritativeStateReady = RestoreAuthoritativeState();
        if (!authoritativeStateReady)
        {
            authorityOwner = null;
            Debug.LogError(
                "Rin authoritative state could not be restored; turns are disabled.");
            return;
        }
        if (!BeginAuthorityLifetime())
        {
            authorityOwner = null;
            authoritativeStateReady = false;
            Debug.LogError(
                "Rin Host generation could not be advanced; turns are disabled.");
            return;
        }
        activeScene = SceneManager.GetActiveScene().handle;
        SceneManager.sceneLoaded += OnSceneLoaded;
        DontDestroyOnLoad(gameObject);
    }

    private void Update()
    {
        while (true)
        {
            Action work;
            lock (authorityQueue)
            {
                if (authorityQueue.Count == 0) break;
                work = authorityQueue.Dequeue();
            }
            work();
        }
    }

    private void OnDestroy()
    {
        if (!ReferenceEquals(authorityOwner, this)) return;
        authorityOwner = null;
        SceneManager.sceneLoaded -= OnSceneLoaded;
        if (!authoritativeStateReady) return;
        actionGate.ReplaceAuthority(
            "The Unity Host was destroyed before the action reached a terminal result.");
        PersistCurrentState();
    }

    private void OnSceneLoaded(Scene scene, LoadSceneMode mode)
    {
        if (scene.handle == activeScene) return;
        activeScene = scene.handle;
        if (!AdvanceEpoch(false))
        {
            authoritativeStateReady = false;
            Debug.LogError("Rin scene authority could not be advanced.");
        }
    }

    public void ConfigureHost(IRinUnityHost value)
    {
        host = value;
    }

    public void RequestTurn()
    {
        if (!authoritativeStateReady)
        {
            Debug.LogError("Rin turn refused until state recovery succeeds.");
            return;
        }
        if (rin == null || !rin.IsConfigured)
        {
            Debug.LogError("RinClient is not configured.");
            return;
        }
        if (host == null)
        {
            Debug.LogError("A component implementing IRinUnityHost is required.");
            return;
        }
        if (turnRunning)
        {
            Debug.LogWarning("A Rin turn is already running.");
            return;
        }
        turnRunning = true;
        StartCoroutine(RunTurn());
    }

    // Call after loading or rolling back authoritative game state. A world
    // load changes world; loading an earlier save also changes timeline.
    public bool AdvanceEpoch(bool timelineChanged)
    {
        if (!authoritativeStateReady || worldEpoch >= MaxJsonInteger ||
            (timelineChanged && timelineEpoch >= MaxJsonInteger))
        {
            return false;
        }
        worldEpoch++;
        if (timelineChanged) timelineEpoch++;
        actionGate.ReplaceAuthority(
            timelineChanged
                ? "The Unity timeline changed while the action was running."
                : "The Unity scene changed while the action was running.");
        if (!authoritativeStateReady || !PersistCurrentState())
        {
            authoritativeStateReady = false;
            return false;
        }
        return true;
    }

    public void ObserveAuthoritativeClock(string clock, long value)
    {
        if (!authoritativeStateReady || activeRun == null ||
            value < 0 || value > MaxJsonInteger)
        {
            return;
        }
        var deadline = activeRun.invocation.deadline;
        if (deadline != null && deadline.clock == clock && value > deadline.value)
        {
            actionGate.ReplaceAuthority(
                "The Unity action exceeded its game-authored deadline.");
        }
    }

    private IEnumerator RunTurn()
    {
        try
        {
            MutationResult created = null;
            yield return rin.CreateSession(
                BuildCreateRequest(),
                value => created = value);
            if (created == null) yield break;

            var drained = false;
            yield return DrainOutbox(value => drained = value);
            if (!drained) yield break;

            if (pendingTurn == null && !CreatePendingTurn()) yield break;
            yield return ResumePendingTurn();
        }
        finally
        {
            turnRunning = false;
        }
    }

    private bool CreatePendingTurn()
    {
        if (operationSequence >= MaxJsonInteger)
        {
            Debug.LogError("Rin operation sequence is exhausted.");
            return false;
        }
        var next = operationSequence + 1;
        var epoch = CurrentEpoch();
        var input = host.CaptureTurn(next, epoch.Copy());
        string error;
        if (!ValidateTurn(input, out error))
        {
            Debug.LogError("Invalid Rin turn: " + error);
            return false;
        }
        var operationId =
            "unity.operation." + runId + "." +
            next.ToString(CultureInfo.InvariantCulture);
        var windowId =
            "unity.window." + runId + "." +
            next.ToString(CultureInfo.InvariantCulture);
        var offers = new ActionOffer[input.offers.Length];
        var offerArguments = new string[input.offers.Length];
        var offerIds = new HashSet<string>();
        for (var index = 0; index < input.offers.Length; index++)
        {
            var template = input.offers[index];
            var offerId = string.IsNullOrEmpty(template.offer_id)
                ? operationId + ".offer." +
                    (index + 1).ToString(CultureInfo.InvariantCulture)
                : template.offer_id;
            if (!RinUnityIds.IsValid(offerId) || !offerIds.Add(offerId))
            {
                Debug.LogError("Rin action Offer IDs must be valid and unique.");
                return false;
            }
            offers[index] = new ActionOffer
            {
                offer_id = offerId,
                decision_window_id = windowId,
                actor_id = input.actor_id,
                capability = template.capability,
                descriptor_digest = template.descriptor_digest,
                description = template.description,
                argumentsJson = template.arguments_json,
                targets = template.targets ?? new HostRef[0],
                expected_epoch = epoch.Copy(),
                observation_seq = input.observation_seq,
                deadline = new Timepoint
                {
                    clock = input.clock,
                    value = input.deadline,
                },
            };
            offerArguments[index] = template.arguments_json;
        }
        var requestId =
            "unity.propose." + runId + "." +
            next.ToString(CultureInfo.InvariantCulture);
        pendingTurn = new PendingTurnState
        {
            version = 1,
            operation_id = operationId,
            offer_arguments_json = offerArguments,
            observation = new ObserveRequest
            {
                session_id = SessionId(),
                request_id = "unity.observe." + runId + "." +
                    next.ToString(CultureInfo.InvariantCulture),
                event_id = "unity.event." + runId + "." +
                    next.ToString(CultureInfo.InvariantCulture),
                tick = input.tick,
                observer_ids = new[] { input.actor_id },
                source = input.source,
                kind = input.observation_kind,
                summary = input.observation_summary,
                importance = input.importance,
                epoch = epoch.Copy(),
                observation_seq = input.observation_seq,
            },
            request = new ProposeRequest
            {
                session_id = SessionId(),
                request_id = requestId,
                actor_id = input.actor_id,
                tick = input.tick,
                intent = input.intent,
                decision_window = new DecisionWindow
                {
                    id = windowId,
                    mode = "sequential",
                    epoch = epoch.Copy(),
                    observation_seq = input.observation_seq,
                    opened_at = new Timepoint
                    {
                        clock = input.clock,
                        value = input.opened_at,
                    },
                    deadline = new Timepoint
                    {
                        clock = input.clock,
                        value = input.deadline,
                    },
                    actor_ids = new[] { input.actor_id },
                },
                offers = offers,
            },
        };
        operationSequence = next;
        observationSequence = input.observation_seq;
        lastAuthoritativeTick = input.tick;
        if (PersistCurrentState()) return true;

        pendingTurn = null;
        operationSequence--;
        Debug.LogError("Rin Pending Turn could not be persisted.");
        return false;
    }

    private IEnumerator ResumePendingTurn()
    {
        var observed = (MutationResult)null;
        yield return rin.Observe(pendingTurn.observation, value => observed = value);
        if (observed == null) yield break;

        ProposalResult resolved = null;
        yield return rin.Propose(pendingTurn.request, value => resolved = value);
        if (resolved == null || resolved.proposal == null) yield break;

        ActionOffer offered;
        string error;
        if (!TryResolveOfferedAction(resolved.proposal, out offered, out error))
        {
            Debug.LogError("Rin returned an invalid proposal: " + error);
            yield break;
        }

        AppliedMarker marker;
        if (applied.TryGetValue(pendingTurn.operation_id, out marker))
        {
            if (!CompletePendingTurn(marker)) yield break;
        }
        else
        {
            var invocation = RinUnityOfferBinding.Invocation(
                pendingTurn.operation_id,
                offered);
            if (!RinUnityOfferBinding.EpochEquals(
                invocation.expected_epoch,
                CurrentEpoch()))
            {
                marker = CreateMarker(
                    pendingTurn.operation_id,
                    resolved.proposal,
                    invocation,
                    RinHostActionResult.Rejected(
                        "The Unity Host rejected an offer from a replaced authority."));
                applied.Add(marker.operation_id, marker);
                if (!CompletePendingTurn(marker)) yield break;
            }
            else
            {
                activeRun = new ActiveRunState
                {
                    operation_id = pendingTurn.operation_id,
                    proposal = resolved.proposal,
                    invocation = invocation,
                    arguments_json = invocation.argumentsJson,
                };
                if (!PersistCurrentState())
                {
                    activeRun = null;
                    Debug.LogError(
                        "Rin Active Run could not be persisted before execution.");
                    yield break;
                }

                var actionFinished = false;
                actionGate.Begin(
                    completed => host.BeginAction(invocation, completed),
                    result =>
                    {
                        FinishActiveRun(result);
                        actionFinished = true;
                    },
                    Dispatch);
                while (!actionFinished) yield return null;
                if (!authoritativeStateReady) yield break;
            }
        }

        var drained = false;
        yield return DrainOutbox(value => drained = value);
    }

    private IEnumerator DrainOutbox(Action<bool> completed)
    {
        while (reportOutbox.Count != 0)
        {
            var entry = reportOutbox[0];
            RinUnityStateValidation.RestoreArguments(entry);
            MutationResult result = null;
            yield return rin.ReportAction(entry.request, value => result = value);
            if (result == null)
            {
                completed(false);
                yield break;
            }
            reportOutbox.RemoveAt(0);
            if (!PersistCurrentState())
            {
                reportOutbox.Insert(0, entry);
                authoritativeStateReady = false;
                completed(false);
                yield break;
            }
        }
        completed(true);
    }

    private bool BeginAuthorityLifetime()
    {
        if (hostEpoch >= MaxJsonInteger || timelineEpoch >= MaxJsonInteger)
        {
            return false;
        }
        hostEpoch++;
        timelineEpoch++;
        if (activeRun == null) return PersistCurrentState();

        var interrupted = new RinHostActionResult
        {
            accepted = true,
            status = "outcome-unknown",
            summary =
                "The Unity domain reloaded before the action reached a durable terminal result.",
        };
        var marker = CreateMarker(
            activeRun.operation_id,
            activeRun.proposal,
            activeRun.invocation,
            interrupted);
        if (!applied.ContainsKey(marker.operation_id))
        {
            applied.Add(marker.operation_id, marker);
        }
        return CompletePendingTurn(marker);
    }

    private void FinishActiveRun(RinHostActionResult result)
    {
        if (activeRun == null) return;
        var marker = CreateMarker(
            activeRun.operation_id,
            activeRun.proposal,
            activeRun.invocation,
            result);
        AppliedMarker existing;
        if (applied.TryGetValue(marker.operation_id, out existing))
        {
            marker = existing;
        }
        else
        {
            applied.Add(marker.operation_id, marker);
        }
        if (!CompletePendingTurn(marker))
        {
            authoritativeStateReady = false;
            Debug.LogError(
                "Rin action result could not be persisted; the next domain " +
                "lifetime will reconcile it as outcome-unknown.");
        }
    }

    private AppliedMarker CreateMarker(
        string operationId,
        ActionProposal proposal,
        ActionInvocation invocation,
        RinHostActionResult result)
    {
        return new AppliedMarker
        {
            operation_id = operationId,
            proposal_id = proposal.id,
            arguments_json = invocation.argumentsJson,
            request = BuildReport(operationId, proposal, invocation, result),
        };
    }

    private bool CompletePendingTurn(AppliedMarker marker)
    {
        foreach (var existing in reportOutbox)
        {
            if (existing.key == marker.operation_id)
            {
                pendingTurn = null;
                activeRun = null;
                return PersistCurrentState();
            }
        }
        if (reportOutbox.Count >= MaxOutboxEntries)
        {
            Debug.LogError("The Rin Unity Outcome Outbox is full.");
            return false;
        }
        reportOutbox.Add(new ReportOutboxEntry
        {
            key = marker.operation_id,
            arguments_json = marker.arguments_json,
            request = marker.request,
        });
        pendingTurn = null;
        activeRun = null;
        TrimAppliedMarkers();
        return PersistCurrentState();
    }

    private void Dispatch(Action work)
    {
        if (Thread.CurrentThread.ManagedThreadId == authorityThread)
        {
            work();
            return;
        }
        lock (authorityQueue)
        {
            authorityQueue.Enqueue(work);
        }
    }

    private bool TryResolveOfferedAction(
        ActionProposal proposal,
        out ActionOffer offered,
        out string error)
    {
        offered = null;
        error = "";
        if (proposal.session_id != pendingTurn.request.session_id ||
            proposal.request_id != pendingTurn.request.request_id ||
            proposal.actor_id != pendingTurn.request.actor_id ||
            proposal.tick != pendingTurn.request.tick ||
            !RinUnityOfferBinding.DecisionWindowEquals(
                proposal.decision_window,
                pendingTurn.request.decision_window) ||
            proposal.action == null)
        {
            error = "proposal identity does not match the durable Pending Turn";
            return false;
        }
        foreach (var candidate in pendingTurn.request.offers)
        {
            if (candidate.offer_id == proposal.action.offer_id)
            {
                if (!RinUnityOfferBinding.Matches(candidate, proposal.action))
                {
                    error = "proposal changed the durable action binding";
                    return false;
                }
                offered = candidate;
                return true;
            }
        }
        error = "proposal selected an action that the host did not offer";
        return false;
    }

    private ReportActionRequest BuildReport(
        string operationId,
        ActionProposal proposal,
        ActionInvocation invocation,
        RinHostActionResult result)
    {
        var suffix = operationId.Substring(
            "unity.operation.".Length);
        var report = new ActionReport
        {
            proposal_id = proposal.id,
            event_id = "unity.action." + suffix,
            decision = result.accepted ? "accepted" : "rejected",
            summary = NonEmpty(
                result.summary,
                result.accepted
                    ? "The Unity host completed the offered action."
                    : "The Unity host rejected the offered action."),
        };
        if (result.accepted)
        {
            var occurredAt = result.occurred_at > 0
                ? result.occurred_at
                : proposal.decision_window.opened_at.value;
            report.invocation = invocation;
            report.run = new ActionRun
            {
                operation_id = invocation.operation_id,
                status = TerminalStatus(result.status),
                progress_seq = 1,
                progress = TerminalStatus(result.status) == "succeeded" ? 100 : 0,
                updated_at = new Timepoint
                {
                    clock = proposal.decision_window.opened_at.clock,
                    value = occurredAt,
                },
                message = report.summary,
            };
            report.outcome = new ActionOutcome
            {
                operation_id = invocation.operation_id,
                status = TerminalStatus(result.status),
                summary = report.summary,
                code = result.code,
                epoch = invocation.expected_epoch.Copy(),
                world_seq = result.world_seq > 0
                    ? result.world_seq
                    : observationSequence,
                occurred_at = report.run.updated_at.Copy(),
                evidence = result.evidence ?? new HostRef[0],
            };
        }
        return new ReportActionRequest
        {
            session_id = SessionId(),
            request_id = "unity.report." + suffix,
            tick = proposal.tick,
            report = report,
        };
    }

    private CreateSessionRequest BuildCreateRequest()
    {
        return new CreateSessionRequest
        {
            request_id = "unity.create." + runId,
            session_id = SessionId(),
            binding = new RinBinding
            {
                game_id = gameId,
                content_id = contentId,
                content_version = contentVersion,
                content_hash = contentHash,
            },
            actors = new[]
            {
                new ActorSeed
                {
                    id = actorId,
                    kind = "npc",
                    display_name = actorDisplayName,
                    think_every_ticks = 1,
                },
            },
        };
    }

    private bool RestoreAuthoritativeState()
    {
        var loaded = stateFile.Load();
        if (loaded != null)
        {
            return Hydrate(loaded);
        }
        if (stateFile.PrimaryExists)
        {
            return false;
        }
        var backup = stateFile.LoadBackup();
        if (backup != null)
        {
            if (!Hydrate(backup)) return false;
            return PersistCurrentState();
        }
        if (stateFile.TemporaryExists)
        {
            return false;
        }

        runId = "r" + Guid.NewGuid().ToString("N");
        hostEpoch = worldEpoch = timelineEpoch = 1;
        operationSequence = observationSequence = lastAuthoritativeTick = 0;
        pendingTurn = null;
        activeRun = null;
        applied.Clear();
        reportOutbox.Clear();
        return PersistCurrentState();
    }

    private bool Hydrate(DurableState state)
    {
        if (state == null || state.schemaVersion != StateSchemaVersion ||
            !RinUnityIds.IsValid(state.runId) ||
            state.gameId != gameId ||
            state.contentId != contentId ||
            state.contentVersion != contentVersion ||
            state.contentHash != contentHash ||
            state.worldId != worldId ||
            state.actorId != actorId ||
            state.operationSequence < 0 || state.hostEpoch <= 0 ||
            state.worldEpoch <= 0 || state.timelineEpoch <= 0 ||
            state.operationSequence > MaxJsonInteger ||
            state.hostEpoch > MaxJsonInteger ||
            state.worldEpoch > MaxJsonInteger ||
            state.timelineEpoch > MaxJsonInteger ||
            state.observationSequence < 0 ||
            state.observationSequence > MaxJsonInteger ||
            state.lastAuthoritativeTick < 0 ||
            state.lastAuthoritativeTick > MaxJsonInteger)
        {
            return false;
        }
        runId = state.runId;
        operationSequence = state.operationSequence;
        hostEpoch = state.hostEpoch;
        worldEpoch = state.worldEpoch;
        timelineEpoch = state.timelineEpoch;
        observationSequence = state.observationSequence;
        lastAuthoritativeTick = state.lastAuthoritativeTick;
        pendingTurn = state.pendingTurn;
        activeRun = state.activeRun;
        applied.Clear();
        reportOutbox.Clear();

        var markers = state.applied ?? new AppliedMarker[0];
        var entries = state.reportOutbox ?? new ReportOutboxEntry[0];
        if (markers.Length > MaxAppliedMarkers ||
            entries.Length > MaxOutboxEntries ||
            (pendingTurn != null &&
                !RinUnityStateValidation.Pending(
                    runId,
                    SessionId(),
                    pendingTurn)) ||
            (activeRun != null &&
                (pendingTurn == null ||
                    activeRun.operation_id != pendingTurn.operation_id)))
        {
            return false;
        }
        if (pendingTurn != null &&
            !RinUnityStateValidation.RestorePendingArguments(pendingTurn))
        {
            return false;
        }
        if (activeRun != null)
        {
            activeRun.invocation.argumentsJson = activeRun.arguments_json;
            if (!RinUnityStateValidation.Active(pendingTurn, activeRun))
            {
                return false;
            }
        }
        foreach (var marker in markers)
        {
            if (!RinUnityReportValidation.Marker(SessionId(), marker) ||
                applied.ContainsKey(marker.operation_id))
            {
                return false;
            }
            RinUnityStateValidation.RestoreArguments(marker);
            applied.Add(marker.operation_id, marker);
        }
        if (activeRun != null && applied.ContainsKey(activeRun.operation_id))
        {
            return false;
        }
        var outboxKeys = new HashSet<string>();
        foreach (var entry in entries)
        {
            if (!RinUnityReportValidation.Outbox(SessionId(), entry) ||
                !outboxKeys.Add(entry.key))
            {
                return false;
            }
            RinUnityStateValidation.RestoreArguments(entry);
            reportOutbox.Add(entry);
        }
        return true;
    }

    private bool PersistCurrentState()
    {
        return stateFile.Save(new DurableState
        {
            schemaVersion = StateSchemaVersion,
            runId = runId,
            gameId = gameId,
            contentId = contentId,
            contentVersion = contentVersion,
            contentHash = contentHash,
            worldId = worldId,
            actorId = actorId,
            operationSequence = operationSequence,
            hostEpoch = hostEpoch,
            worldEpoch = worldEpoch,
            timelineEpoch = timelineEpoch,
            observationSequence = observationSequence,
            lastAuthoritativeTick = lastAuthoritativeTick,
            pendingTurn = pendingTurn,
            activeRun = activeRun,
            applied = Values(applied),
            reportOutbox = reportOutbox.ToArray(),
        });
    }

    private Epoch CurrentEpoch()
    {
        return new Epoch
        {
            session_id = SessionId(),
            world_id = worldId,
            host = hostEpoch,
            world = worldEpoch,
            timeline = timelineEpoch,
        };
    }

    private string SessionId()
    {
        return "unity.session." + runId;
    }

    private bool ValidateTurn(RinTurnInput input, out string error)
    {
        error = "";
        if (input == null || input.offers == null ||
            input.offers.Length == 0 || input.offers.Length > 32)
        {
            error = "CaptureTurn must return 1-32 action offers";
            return false;
        }
        if (!RinUnityIds.IsValid(input.actor_id) ||
            input.actor_id != actorId ||
            input.tick < lastAuthoritativeTick ||
            input.tick > MaxJsonInteger ||
            input.observation_seq <= observationSequence ||
            input.observation_seq > MaxJsonInteger ||
            input.opened_at < 0 || input.deadline <= input.opened_at ||
            input.deadline > MaxJsonInteger ||
            (input.clock != "event" && input.clock != "step" &&
                input.clock != "realtime") ||
            string.IsNullOrWhiteSpace(input.intent) ||
            string.IsNullOrWhiteSpace(input.observation_summary))
        {
            error = "turn identity, time, or description is invalid";
            return false;
        }
        foreach (var offer in input.offers)
        {
            if (offer == null || offer.capability == null ||
                !RinUnityIds.IsValid(offer.capability.id) ||
                string.IsNullOrEmpty(offer.capability.version) ||
                !RinUnityIds.IsDigest(offer.descriptor_digest) ||
                string.IsNullOrWhiteSpace(offer.description) ||
                !RinUnityJson.IsValidObject(offer.arguments_json) ||
                (!string.IsNullOrEmpty(offer.offer_id) &&
                    !RinUnityIds.IsValid(offer.offer_id)))
            {
                error = "an offer capability or descriptor is invalid";
                return false;
            }
        }
        return true;
    }

    private bool ValidConfiguration()
    {
        return RinUnityIds.IsValid(gameId) &&
            RinUnityIds.IsValid(contentId) &&
            RinUnityIds.IsValid(worldId) &&
            RinUnityIds.IsValid(actorId) &&
            !string.IsNullOrWhiteSpace(contentVersion) &&
            contentVersion.Length <= 64 &&
            RinUnityIds.IsDigest(contentHash) &&
            !string.IsNullOrWhiteSpace(actorDisplayName);
    }

    private void TrimAppliedMarkers()
    {
        if (applied.Count <= MaxAppliedMarkers) return;
        var protectedKeys = new HashSet<string>();
        foreach (var entry in reportOutbox) protectedKeys.Add(entry.key);
        foreach (var key in new List<string>(applied.Keys))
        {
            if (applied.Count <= MaxAppliedMarkers) return;
            if (!protectedKeys.Contains(key)) applied.Remove(key);
        }
    }

    private static AppliedMarker[] Values(
        Dictionary<string, AppliedMarker> source)
    {
        var values = new AppliedMarker[source.Count];
        source.Values.CopyTo(values, 0);
        Array.Sort(
            values,
            (left, right) =>
                string.CompareOrdinal(left.operation_id, right.operation_id));
        return values;
    }

    private static string TerminalStatus(string value)
    {
        switch (value)
        {
            case "failed":
            case "cancelled":
            case "interrupted":
            case "stale":
            case "outcome-unknown":
                return value;
            default:
                return "succeeded";
        }
    }

    private static string NonEmpty(string value, string fallback)
    {
        return string.IsNullOrWhiteSpace(value) ? fallback : value;
    }

}

[Serializable]
internal sealed class DurableState
{
    public int schemaVersion;
    public string runId;
    public string gameId;
    public string contentId;
    public string contentVersion;
    public string contentHash;
    public string worldId;
    public string actorId;
    public long operationSequence;
    public long hostEpoch;
    public long worldEpoch;
    public long timelineEpoch;
    public long observationSequence;
    public long lastAuthoritativeTick;
    public PendingTurnState pendingTurn;
    public ActiveRunState activeRun;
    public AppliedMarker[] applied;
    public ReportOutboxEntry[] reportOutbox;
}

[Serializable]
internal sealed class PendingTurnState
{
    public int version;
    public string operation_id;
    public string[] offer_arguments_json;
    public ObserveRequest observation;
    public ProposeRequest request;
}

[Serializable]
internal sealed class ActiveRunState
{
    public string operation_id;
    public string arguments_json;
    public ActionProposal proposal;
    public ActionInvocation invocation;
}

[Serializable]
internal sealed class AppliedMarker
{
    public string operation_id;
    public string proposal_id;
    public string arguments_json;
    public ReportActionRequest request;
}

[Serializable]
internal sealed class ReportOutboxEntry
{
    public string key;
    public string arguments_json;
    public ReportActionRequest request;
}

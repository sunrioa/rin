using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using UnityEngine;

// The game implements this interface at its authority boundary. Execute must
// treat invocation.operation_id as an idempotency key if the adapter is used
// for world mutation: a process can stop after the effect but before the
// durable outbox write.
public interface IRinUnityHost
{
    RinTurnInput CaptureTurn(long operationSequence, Epoch epoch);
    RinHostActionResult Execute(ActionInvocation invocation);
}

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
    private const int StateSchemaVersion = 2;
    private const int MaxStateBytes = 1024 * 1024;
    private const int MaxAppliedMarkers = 256;
    private const int MaxOutboxEntries = 64;

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
    private bool authoritativeStateReady;
    private bool turnRunning;
    private string statePath;

    private void Awake()
    {
        statePath = Path.Combine(
            Application.persistentDataPath,
            "rin",
            "default.json");
        host = hostComponent as IRinUnityHost;
        authoritativeStateReady = RestoreAuthoritativeState();
        if (!authoritativeStateReady)
        {
            Debug.LogError(
                "Rin authoritative state could not be restored; turns are disabled.");
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
        if (!authoritativeStateReady || pendingTurn != null ||
            reportOutbox.Count != 0 || worldEpoch == long.MaxValue ||
            (timelineChanged && timelineEpoch == long.MaxValue))
        {
            return false;
        }
        worldEpoch++;
        if (timelineChanged) timelineEpoch++;
        return PersistCurrentState();
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
        if (operationSequence == long.MaxValue)
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
        for (var index = 0; index < input.offers.Length; index++)
        {
            var template = input.offers[index];
            offers[index] = new ActionOffer
            {
                offer_id = string.IsNullOrEmpty(template.offer_id)
                    ? operationId + ".offer." +
                        (index + 1).ToString(CultureInfo.InvariantCulture)
                    : template.offer_id,
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
        }
        var requestId =
            "unity.propose." + runId + "." +
            next.ToString(CultureInfo.InvariantCulture);
        pendingTurn = new PendingTurnState
        {
            version = 1,
            operation_id = operationId,
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
        if (!applied.TryGetValue(pendingTurn.operation_id, out marker))
        {
            var invocation = BuildInvocation(pendingTurn.operation_id, offered);
            RinHostActionResult result;
            try
            {
                result = host.Execute(invocation);
            }
            catch (Exception hostError)
            {
                result = RinHostActionResult.Rejected(
                    "The host rejected the action after an execution error: " +
                    hostError.GetType().Name);
            }
            if (result == null)
            {
                result = RinHostActionResult.Rejected(
                    "The host returned no action result.");
            }
            marker = new AppliedMarker
            {
                operation_id = pendingTurn.operation_id,
                proposal_id = resolved.proposal.id,
                request = BuildReport(resolved.proposal, invocation, result),
            };
            applied.Add(marker.operation_id, marker);
        }

        reportOutbox.Add(new ReportOutboxEntry
        {
            key = marker.operation_id,
            request = marker.request,
        });
        pendingTurn = null;
        TrimAppliedMarkers();
        if (!PersistCurrentState())
        {
            Debug.LogError(
                "Rin action result could not be persisted; Execute must remain " +
                "idempotent for this operation_id.");
            yield break;
        }

        var drained = false;
        yield return DrainOutbox(value => drained = value);
    }

    private IEnumerator DrainOutbox(Action<bool> completed)
    {
        while (reportOutbox.Count != 0)
        {
            var entry = reportOutbox[0];
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
                completed(false);
                yield break;
            }
        }
        completed(true);
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
            proposal.decision_window == null ||
            proposal.decision_window.id !=
                pendingTurn.request.decision_window.id ||
            proposal.action == null)
        {
            error = "proposal identity does not match the durable Pending Turn";
            return false;
        }
        foreach (var candidate in pendingTurn.request.offers)
        {
            if (candidate.offer_id == proposal.action.offer_id)
            {
                offered = candidate;
                return true;
            }
        }
        error = "proposal selected an action that the host did not offer";
        return false;
    }

    private static ActionInvocation BuildInvocation(
        string operationId,
        ActionOffer offer)
    {
        return new ActionInvocation
        {
            operation_id = operationId,
            offer_id = offer.offer_id,
            decision_window_id = offer.decision_window_id,
            actor_id = offer.actor_id,
            capability = offer.capability,
            descriptor_digest = offer.descriptor_digest,
            argumentsJson = offer.argumentsJson,
            targets = offer.targets,
            expected_epoch = offer.expected_epoch.Copy(),
            observation_seq = offer.observation_seq,
            deadline = offer.deadline.Copy(),
        };
    }

    private ReportActionRequest BuildReport(
        ActionProposal proposal,
        ActionInvocation invocation,
        RinHostActionResult result)
    {
        var suffix = pendingTurn.operation_id.Substring(
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
                progress = 100,
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
                epoch = CurrentEpoch(),
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
        var loaded = LoadState();
        if (loaded != null)
        {
            return Hydrate(loaded);
        }
        if (File.Exists(statePath))
        {
            return false;
        }
        var backup = LoadStateFile(statePath + ".bak");
        if (backup != null)
        {
            if (!Hydrate(backup)) return false;
            return PersistCurrentState();
        }
        if (File.Exists(statePath + ".tmp"))
        {
            return false;
        }

        runId = "r" + Guid.NewGuid().ToString("N");
        hostEpoch = worldEpoch = timelineEpoch = 1;
        operationSequence = observationSequence = lastAuthoritativeTick = 0;
        pendingTurn = null;
        applied.Clear();
        reportOutbox.Clear();
        return PersistCurrentState();
    }

    private DurableState LoadState()
    {
        return LoadStateFile(statePath);
    }

    private static DurableState LoadStateFile(string path)
    {
        try
        {
            if (!File.Exists(path)) return null;
            var info = new FileInfo(path);
            if (info.Length <= 0 || info.Length > MaxStateBytes) return null;
            return JsonUtility.FromJson<DurableState>(
                File.ReadAllText(path, Encoding.UTF8));
        }
        catch (Exception)
        {
            return null;
        }
    }

    private bool Hydrate(DurableState state)
    {
        if (state == null || state.schemaVersion != StateSchemaVersion ||
            !RinUnityIds.IsValid(state.runId) ||
            state.operationSequence < 0 || state.hostEpoch <= 0 ||
            state.worldEpoch <= 0 || state.timelineEpoch <= 0 ||
            state.observationSequence < 0 || state.lastAuthoritativeTick < 0)
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
        applied.Clear();
        reportOutbox.Clear();

        var markers = state.applied ?? new AppliedMarker[0];
        var entries = state.reportOutbox ?? new ReportOutboxEntry[0];
        if (markers.Length > MaxAppliedMarkers ||
            entries.Length > MaxOutboxEntries ||
            (pendingTurn != null && !ValidPendingTurn(pendingTurn)))
        {
            return false;
        }
        foreach (var marker in markers)
        {
            if (marker == null || !RinUnityIds.IsValid(marker.operation_id) ||
                marker.request == null || applied.ContainsKey(marker.operation_id))
            {
                return false;
            }
            applied.Add(marker.operation_id, marker);
        }
        foreach (var entry in entries)
        {
            if (entry == null || !RinUnityIds.IsValid(entry.key) ||
                entry.request == null)
            {
                return false;
            }
            reportOutbox.Add(entry);
        }
        return true;
    }

    private bool PersistCurrentState()
    {
        return Persist(new DurableState
        {
            schemaVersion = StateSchemaVersion,
            runId = runId,
            operationSequence = operationSequence,
            hostEpoch = hostEpoch,
            worldEpoch = worldEpoch,
            timelineEpoch = timelineEpoch,
            observationSequence = observationSequence,
            lastAuthoritativeTick = lastAuthoritativeTick,
            pendingTurn = pendingTurn,
            applied = Values(applied),
            reportOutbox = reportOutbox.ToArray(),
        });
    }

    private bool Persist(DurableState state)
    {
        var temporary = statePath + ".tmp";
        var backup = statePath + ".bak";
        try
        {
            var directory = Path.GetDirectoryName(statePath);
            if (string.IsNullOrEmpty(directory)) return false;
            Directory.CreateDirectory(directory);
            var bytes = Encoding.UTF8.GetBytes(JsonUtility.ToJson(state));
            if (bytes.Length <= 0 || bytes.Length > MaxStateBytes) return false;
            using (var stream = new FileStream(
                temporary,
                FileMode.Create,
                FileAccess.Write,
                FileShare.None))
            {
                stream.Write(bytes, 0, bytes.Length);
                stream.Flush(true);
            }
            if (File.Exists(statePath))
            {
                if (File.Exists(backup)) File.Delete(backup);
                File.Replace(temporary, statePath, backup);
            }
            else
            {
                File.Move(temporary, statePath);
            }
            return true;
        }
        catch (Exception error)
        {
            Debug.LogError("Could not persist Rin state: " + error.Message);
            return false;
        }
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
            input.observation_seq <= observationSequence ||
            input.opened_at < 0 || input.deadline <= input.opened_at ||
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
                !IsDigest(offer.descriptor_digest) ||
                string.IsNullOrWhiteSpace(offer.description))
            {
                error = "an offer capability or descriptor is invalid";
                return false;
            }
        }
        return true;
    }

    private static bool ValidPendingTurn(PendingTurnState value)
    {
        return value.version == 1 &&
            RinUnityIds.IsValid(value.operation_id) &&
            value.observation != null &&
            value.request != null &&
            value.request.decision_window != null &&
            value.request.offers != null &&
            value.request.offers.Length > 0;
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

    private static bool IsDigest(string value)
    {
        if (value == null || value.Length != 64) return false;
        foreach (var character in value)
        {
            if (!((character >= '0' && character <= '9') ||
                (character >= 'a' && character <= 'f')))
            {
                return false;
            }
        }
        return true;
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

    [Serializable]
    private sealed class DurableState
    {
        public int schemaVersion;
        public string runId;
        public long operationSequence;
        public long hostEpoch;
        public long worldEpoch;
        public long timelineEpoch;
        public long observationSequence;
        public long lastAuthoritativeTick;
        public PendingTurnState pendingTurn;
        public AppliedMarker[] applied;
        public ReportOutboxEntry[] reportOutbox;
    }

    [Serializable]
    private sealed class PendingTurnState
    {
        public int version;
        public string operation_id;
        public ObserveRequest observation;
        public ProposeRequest request;
    }

    [Serializable]
    private sealed class AppliedMarker
    {
        public string operation_id;
        public string proposal_id;
        public ReportActionRequest request;
    }

    [Serializable]
    private sealed class ReportOutboxEntry
    {
        public string key;
        public ReportActionRequest request;
    }
}

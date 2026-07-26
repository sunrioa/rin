using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Rin.Client;

namespace RinNpcExample;

public sealed class BepInExWorkflowState : IWorkflowFallbackStore
{
    private const int CurrentVersion = 1;
    private const int MaxOutcomes = 32;
    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        WriteIndented = false,
    };

    private readonly object gate = new();
    private readonly string path;
    private StateData state;
    private CreateSessionRequest? stagedCreate;
    private JsonElement? stagedObserve;

    private BepInExWorkflowState(string path, StateData state)
    {
        this.path = path;
        this.state = state;
    }

    public string SessionId => state.SessionId;
    public long Sequence => state.Sequence;
    public CreateSessionRequest? CreateRequest => state.CreateRequest;
    public object? PendingObserve => state.PendingObserve;
    public int QuestStage => state.QuestStage;
    public string Diagnostics =>
        "profile=advisory session=" + state.SessionId +
        " sequence=" + state.Sequence +
        " quest_stage=" + state.QuestStage +
        " pending=" + (state.Pending is not null) +
        " outbox=" + state.Outcomes.Count;

    public bool ApplyQuestEffect(string operationId, string actionId)
    {
        lock (gate)
        {
            if (state.AppliedGameOperations.Contains(operationId)) return true;
            if (state.Pending?.OperationId != operationId) return false;
            var nextStage = actionId switch
            {
                "offer_quest" when state.QuestStage == 0 => 1,
                "advance_quest" when state.QuestStage == 1 => 2,
                _ => state.QuestStage,
            };
            if (nextStage == state.QuestStage) return false;
            var candidate = CopyState();
            candidate.QuestStage = nextStage;
            candidate.AppliedGameOperations.Add(operationId);
            Persist(candidate);
            return true;
        }
    }

    public static BepInExWorkflowState Open(
        string directory,
        string productName,
        string saveIdentity)
    {
        if (!ValidIdentity(productName, 128) || !ValidIdentity(saveIdentity, 96))
        {
            throw new RinConfigurationException(
                "invalid_save_identity",
                "ProductIdentity and SaveIdentity must be stable, bounded text");
        }
        Directory.CreateDirectory(directory);
        var identityHash = Hex(SHA256.Create().ComputeHash(
            Encoding.UTF8.GetBytes(productName + "\n" + saveIdentity)));
        var path = Path.Combine(directory, "rin-npc-" + identityHash.Substring(0, 24) + ".json");
        if (!File.Exists(path))
        {
            var instance = Guid.NewGuid().ToString("N");
            var created = new StateData
            {
                Version = CurrentVersion,
                SaveIdentity = saveIdentity,
                SessionId = "bepinex." + instance + "." + identityHash.Substring(0, 16),
            };
            var result = new BepInExWorkflowState(path, created);
            result.Persist(created);
            return result;
        }

        var bytes = File.ReadAllBytes(path);
        if (bytes.Length == 0 || bytes.Length > 2_000_000)
        {
            throw new InvalidDataException("Rin workflow state has an invalid size");
        }
        StateData loaded;
        try
        {
            loaded = JsonSerializer.Deserialize<StateData>(bytes, JsonOptions) ??
                throw new InvalidDataException("Rin workflow state is empty");
        }
        catch (JsonException exception)
        {
            throw new InvalidDataException("Rin workflow state is not valid JSON", exception);
        }
        var invalid = InvalidState(loaded, saveIdentity);
        if (invalid is not null)
        {
            throw new InvalidDataException(
                "Rin workflow state is incompatible or malformed: " + invalid);
        }
        return new BepInExWorkflowState(path, loaded);
    }

    public void StageTurnContext(
        CreateSessionRequest createRequest,
        object observe)
    {
        lock (gate)
        {
            if (state.Pending is not null ||
                createRequest.SessionId != state.SessionId)
            {
                throw new RinConfigurationException(
                    "invalid_workflow",
                    "Cannot stage context while another Pending Turn exists");
            }
            stagedCreate = createRequest;
            stagedObserve = JsonSerializer.SerializeToElement(observe, JsonOptions);
        }
    }

    public ValueTask<ProposalAttempt?> LoadAsync(
        CancellationToken cancellationToken = default)
    {
        lock (gate) return new ValueTask<ProposalAttempt?>(state.Pending);
    }

    public ValueTask<bool> CreateAsync(
        ProposalAttempt attempt,
        CancellationToken cancellationToken = default)
    {
        lock (gate)
        {
            if (state.Pending is not null) return new ValueTask<bool>(false);
            if (stagedCreate is null || stagedObserve is null)
            {
                throw new RinConfigurationException(
                    "workflow_context_missing",
                    "Stage the game-owned Create and Observe context before Begin");
            }
            var next = checked(state.Sequence + 1);
            if (attempt.OperationId != state.SessionId + "." + next ||
                attempt.Request.SessionId != state.SessionId)
            {
                throw new RinConfigurationException(
                    "workflow_identity_mismatch",
                    "Pending Turn does not match the staged save identity");
            }
            var candidate = CopyState();
            candidate.Sequence = next;
            candidate.CreateRequest ??= stagedCreate;
            candidate.PendingObserve = stagedObserve;
            candidate.Pending = attempt;
            Persist(candidate);
            stagedCreate = null;
            stagedObserve = null;
            return new ValueTask<bool>(true);
        }
    }

    public ValueTask SaveAsync(
        ProposalAttempt attempt,
        CancellationToken cancellationToken = default)
    {
        lock (gate)
        {
            RequirePending(attempt);
            var candidate = CopyState();
            candidate.Pending = attempt;
            Persist(candidate);
            return default;
        }
    }

    public ValueTask SettleAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        CommitRequest commit,
        Func<CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default) =>
        throw new RinConfigurationException(
            "host_durability_insufficient",
            "Generic BepInEx storage cannot atomically apply a game effect");

    public ValueTask CompleteAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        CommitRequest commit,
        CancellationToken cancellationToken = default)
    {
        lock (gate)
        {
            RequirePending(attempt);
            if (state.Outcomes.Count >= MaxOutcomes)
            {
                throw new InvalidOperationException("Rin Outcome Outbox is full");
            }
            var candidate = CopyState();
            candidate.Outcomes.Add(new OutcomeOutboxEntry(attempt.OperationId, commit));
            candidate.Pending = null;
            candidate.PendingObserve = null;
            Persist(candidate);
            return default;
        }
    }

    public ValueTask CompleteWithFallbackAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        CommitRequest commit,
        object fallbackObserve,
        CancellationToken cancellationToken = default)
    {
        lock (gate)
        {
            RequirePending(attempt);
            if (state.Outcomes.Count >= MaxOutcomes)
            {
                throw new InvalidOperationException("Rin Outcome Outbox is full");
            }
            var candidate = CopyState();
            var safeFallback = JsonSerializer.SerializeToElement(fallbackObserve, JsonOptions);
            candidate.Outcomes.Add(
                new OutcomeOutboxEntry(attempt.OperationId, commit, safeFallback));
            candidate.Pending = null;
            candidate.PendingObserve = null;
            Persist(candidate);
            return default;
        }
    }

    public ValueTask<IReadOnlyList<OutcomeOutboxEntry>> ListAsync(
        CancellationToken cancellationToken = default)
    {
        lock (gate)
        {
            return new ValueTask<IReadOnlyList<OutcomeOutboxEntry>>(
                state.Outcomes.ToArray());
        }
    }

    public ValueTask AcknowledgeAsync(
        OutcomeOutboxEntry entry,
        MutationResult result,
        CancellationToken cancellationToken = default)
    {
        lock (gate)
        {
            var candidate = CopyState();
            var removed = candidate.Outcomes.RemoveAll(outcome =>
                SameEntry(outcome, entry));
            if (removed != 1) throw new InvalidOperationException("Outcome changed before ACK");
            Persist(candidate);
            return default;
        }
    }

    public ValueTask<OutcomeOutboxEntry> ReplaceWithFallbackAsync(
        OutcomeOutboxEntry entry,
        CancellationToken cancellationToken = default)
    {
        lock (gate)
        {
            var candidate = CopyState();
            var index = candidate.Outcomes.FindIndex(outcome => SameEntry(outcome, entry));
            if (index < 0)
            {
                throw new InvalidOperationException("Outcome changed before fallback conversion");
            }
            var converted = candidate.Outcomes[index].AsDegradedObserve();
            candidate.Outcomes[index] = converted;
            Persist(candidate);
            return new ValueTask<OutcomeOutboxEntry>(converted);
        }
    }

    private void RequirePending(ProposalAttempt attempt)
    {
        var pending = state.Pending;
        if (pending is null ||
            pending.Version != attempt.Version ||
            pending.OperationId != attempt.OperationId ||
            pending.Request.SessionId != attempt.Request.SessionId ||
            pending.Request.RequestId != attempt.Request.RequestId ||
            (pending.JobId.Length != 0 && pending.JobId != attempt.JobId))
        {
            throw new InvalidOperationException("Pending Turn changed before persistence");
        }
    }

    private static bool SameEntry(OutcomeOutboxEntry candidate, OutcomeOutboxEntry expected) =>
        candidate.Key == expected.Key &&
        candidate.Commit.RequestId == expected.Commit.RequestId &&
        candidate.IsDegradedObserve == expected.IsDegradedObserve;

    private StateData CopyState() =>
        JsonSerializer.Deserialize<StateData>(
            JsonSerializer.SerializeToUtf8Bytes(state, JsonOptions),
            JsonOptions) ??
        throw new InvalidOperationException("Could not copy Rin workflow state");

    private void Persist(StateData candidate)
    {
        var bytes = JsonSerializer.SerializeToUtf8Bytes(candidate, JsonOptions);
        if (bytes.Length > 2_000_000) throw new InvalidOperationException("Rin state is too large");
        var temporary = path + ".next";
        using (var stream = new FileStream(
            temporary, FileMode.Create, FileAccess.Write, FileShare.None))
        {
            stream.Write(bytes, 0, bytes.Length);
            stream.Flush(flushToDisk: true);
        }
        if (File.Exists(path)) File.Replace(temporary, path, null);
        else File.Move(temporary, path);
        state = candidate;
    }

    private static string Hex(byte[] bytes) =>
        BitConverter.ToString(bytes).Replace("-", string.Empty).ToLowerInvariant();

    private static bool ValidIdentity(string? value, int maximum) =>
        !string.IsNullOrWhiteSpace(value) &&
        value!.Length <= maximum &&
        value.IndexOfAny(new[] { '\0', '\r', '\n' }) < 0;

    private static bool InvalidOutcome(
        OutcomeOutboxEntry? outcome,
        string sessionId)
    {
        if (outcome is null || outcome.Commit is null) return true;
        return !RinIds.IsValid(outcome.Key) ||
            (outcome.IsDegradedObserve && outcome.FallbackObserve is null) ||
            outcome.Commit.SessionId != sessionId ||
            !RinIds.IsValid(outcome.Commit.RequestId) ||
            !RinIds.IsValid(outcome.Commit.EventId);
    }

    private static string? InvalidState(StateData loaded, string saveIdentity)
    {
        if (loaded.Version != CurrentVersion) return "version";
        if (loaded.SaveIdentity != saveIdentity) return "save identity";
        if (!RinIds.IsValid(loaded.SessionId) ||
            !loaded.SessionId.StartsWith("bepinex.", StringComparison.Ordinal))
        {
            return "session identity";
        }
        if (loaded.Sequence < 0) return "sequence";
        if (loaded.QuestStage < 0 || loaded.QuestStage > 2 ||
            loaded.AppliedGameOperations is null ||
            loaded.AppliedGameOperations.Count > 256 ||
            loaded.AppliedGameOperations.Any(id => !RinIds.IsValid(id)) ||
            loaded.AppliedGameOperations.Distinct().Count() !=
                loaded.AppliedGameOperations.Count)
        {
            return "game effect state";
        }
        if (loaded.Outcomes is null || loaded.Outcomes.Count > MaxOutcomes)
        {
            return "outbox bounds";
        }
        if (loaded.Outcomes.Any(outcome => InvalidOutcome(outcome, loaded.SessionId)))
        {
            return "outbox entry";
        }
        if (loaded.Outcomes.Select(outcome => outcome.Key).Distinct().Count() !=
            loaded.Outcomes.Count)
        {
            return "duplicate outbox key";
        }
        if ((loaded.Pending is null) != (loaded.PendingObserve is null))
        {
            return "pending observe";
        }
        if (loaded.Pending is not null && loaded.CreateRequest is null)
        {
            return "pending create";
        }
        if (loaded.CreateRequest is not null &&
            loaded.CreateRequest.SessionId != loaded.SessionId)
        {
            return "create identity";
        }
        if (loaded.Pending is not null &&
            (loaded.Pending.Request is null ||
             loaded.Pending.Version != 1 ||
             loaded.Pending.OperationId != loaded.SessionId + "." + loaded.Sequence ||
             loaded.Pending.Request.SessionId != loaded.SessionId ||
             !RinIds.IsValid(loaded.Pending.Request.RequestId) ||
             (loaded.Pending.JobId.Length != 0 &&
              !RinIds.IsValid(loaded.Pending.JobId))))
        {
            return "pending identity";
        }
        if (loaded.PendingObserve is { } pendingObserve &&
            pendingObserve.ValueKind != JsonValueKind.Object)
        {
            return "pending observe shape";
        }
        return null;
    }

    private sealed class StateData
    {
        public int Version { get; set; }
        public string SaveIdentity { get; set; } = string.Empty;
        public string SessionId { get; set; } = string.Empty;
        public long Sequence { get; set; }
        public CreateSessionRequest? CreateRequest { get; set; }
        public ProposalAttempt? Pending { get; set; }
        public JsonElement? PendingObserve { get; set; }
        public List<OutcomeOutboxEntry> Outcomes { get; set; } = new();
        public int QuestStage { get; set; }
        public List<string> AppliedGameOperations { get; set; } = new();
    }
}

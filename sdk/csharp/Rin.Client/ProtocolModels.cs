using System.Text.Json;
using System.Text.Json.Serialization;

namespace Rin.Client;

public sealed record BoundaryInput(
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("description")] string Description,
    [property: JsonPropertyName("response")] string Response)
{
    [JsonPropertyName("trigger_tags")]
    public IReadOnlyList<string>? TriggerTags { get; init; }
}

public sealed record GoalSeedInput(
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("description")] string Description,
    [property: JsonPropertyName("priority")] int Priority,
    [property: JsonPropertyName("target_progress")] int TargetProgress,
    [property: JsonPropertyName("status")] string Status)
{
    [JsonPropertyName("motivation")]
    public string? Motivation { get; init; }

    [JsonPropertyName("preferred_actions")]
    public IReadOnlyList<string>? PreferredActions { get; init; }

    [JsonPropertyName("progress")]
    public int? Progress { get; init; }
}

public sealed record ActorSeedInput(
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("display_name")] string DisplayName,
    [property: JsonPropertyName("think_every_ticks")] int ThinkEveryTicks)
{
    [JsonPropertyName("traits")]
    public IReadOnlyList<string>? Traits { get; init; }

    [JsonPropertyName("boundaries")]
    public IReadOnlyList<BoundaryInput>? Boundaries { get; init; }

    [JsonPropertyName("goals")]
    public IReadOnlyList<GoalSeedInput>? Goals { get; init; }

    [JsonPropertyName("metadata")]
    public IReadOnlyDictionary<string, string>? Metadata { get; init; }

    [JsonPropertyName("enabled")]
    public bool? Enabled { get; init; }
}

public sealed record ActionSpecInput(
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("description")] string Description)
{
    [JsonPropertyName("target_ids")]
    public IReadOnlyList<string>? TargetIds { get; init; }

    [JsonPropertyName("parameters")]
    public IReadOnlyDictionary<string, string>? Parameters { get; init; }
}

public sealed record CreateSessionRequest(
    [property: JsonPropertyName("request_id")] string RequestId,
    [property: JsonPropertyName("session_id")] string SessionId,
    [property: JsonPropertyName("binding")] RinBinding Binding,
    [property: JsonPropertyName("actors")] IReadOnlyList<ActorSeedInput> Actors)
{
    [JsonPropertyName("protocol_version")]
    public string ProtocolVersion { get; init; } = RinClient.ProtocolVersion;

    [JsonPropertyName("seed")]
    public long? Seed { get; init; }

    [JsonPropertyName("features")]
    public IReadOnlyList<string>? Features { get; init; }
}

public sealed record ProposeRequest(
    [property: JsonPropertyName("session_id")] string SessionId,
    [property: JsonPropertyName("request_id")] string RequestId,
    [property: JsonPropertyName("actor_id")] string ActorId,
    [property: JsonPropertyName("intent")] string Intent,
    [property: JsonPropertyName("candidate_actions")] IReadOnlyList<ActionSpecInput> CandidateActions)
{
    [JsonPropertyName("protocol_version")]
    public string ProtocolVersion { get; init; } = RinClient.ProtocolVersion;

    [JsonPropertyName("tick")]
    public long? Tick { get; init; }

    [JsonPropertyName("tags")]
    public IReadOnlyList<string>? Tags { get; init; }

    [JsonPropertyName("candidate_goals")]
    public IReadOnlyList<GoalSeedInput>? CandidateGoals { get; init; }

    [JsonPropertyName("urgent")]
    public bool? Urgent { get; init; }
}

public sealed record FactInput(
    [property: JsonPropertyName("subject_id")] string SubjectId,
    [property: JsonPropertyName("predicate")] string Predicate,
    [property: JsonPropertyName("object")] string Object)
{
    [JsonPropertyName("visibility")]
    public IReadOnlyList<string>? Visibility { get; init; }

    [JsonPropertyName("confidence")]
    public int? Confidence { get; init; }

    [JsonPropertyName("source_event_id")]
    public string? SourceEventId { get; init; }
}

public sealed record GoalUpdateInput(
    [property: JsonPropertyName("goal_id")] string GoalId)
{
    [JsonPropertyName("progress_delta")]
    public int? ProgressDelta { get; init; }

    [JsonPropertyName("status")]
    public string? Status { get; init; }
}

public sealed record CommitRequest(
    [property: JsonPropertyName("session_id")] string SessionId,
    [property: JsonPropertyName("request_id")] string RequestId,
    [property: JsonPropertyName("proposal_id")] string ProposalId,
    [property: JsonPropertyName("event_id")] string EventId,
    [property: JsonPropertyName("accepted")] bool Accepted)
{
    [JsonPropertyName("protocol_version")]
    public string ProtocolVersion { get; init; } = RinClient.ProtocolVersion;

    [JsonPropertyName("tick")]
    public long? Tick { get; init; }

    [JsonPropertyName("outcome")]
    public string? Outcome { get; init; }

    [JsonPropertyName("tags")]
    public IReadOnlyList<string>? Tags { get; init; }

    [JsonPropertyName("facts")]
    public IReadOnlyList<FactInput>? Facts { get; init; }

    [JsonPropertyName("goal_updates")]
    public IReadOnlyList<GoalUpdateInput>? GoalUpdates { get; init; }
}

public sealed record MutationResult(
    [property: JsonPropertyName("session_id")] string SessionId,
    [property: JsonPropertyName("revision")] long Revision,
    [property: JsonPropertyName("head_hash")] string HeadHash,
    [property: JsonPropertyName("duplicate")] bool Duplicate)
{
    [JsonExtensionData]
    public IDictionary<string, JsonElement>? AdditiveFields { get; init; }
}

public sealed record ActionProposal(
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("session_id")] string SessionId,
    [property: JsonPropertyName("request_id")] string RequestId,
    [property: JsonPropertyName("actor_id")] string ActorId,
    [property: JsonPropertyName("tick")] long Tick,
    [property: JsonPropertyName("based_on_revision")] long BasedOnRevision,
    [property: JsonPropertyName("based_on_head_hash")] string BasedOnHeadHash,
    [property: JsonPropertyName("created_revision")] long CreatedRevision,
    [property: JsonPropertyName("action")] ActionSpecInput Action,
    [property: JsonPropertyName("stance")] string Stance,
    [property: JsonPropertyName("summary")] string Summary,
    [property: JsonPropertyName("rationale")] string Rationale,
    [property: JsonPropertyName("status")] string Status)
{
    [JsonPropertyName("based_on_world_revision")]
    public long? BasedOnWorldRevision { get; init; }

    [JsonPropertyName("policy_source")]
    public string? PolicySource { get; init; }

    [JsonPropertyName("recalled_memory_ids")]
    public IReadOnlyList<string>? RecalledMemoryIds { get; init; }

    [JsonPropertyName("goal_id")]
    public string? GoalId { get; init; }

    [JsonPropertyName("boundary_id")]
    public string? BoundaryId { get; init; }

    [JsonPropertyName("proposed_goal")]
    public JsonElement? ProposedGoal { get; init; }

    [JsonPropertyName("outcome_event_id")]
    public string? OutcomeEventId { get; init; }

    [JsonPropertyName("outcome_tick")]
    public long? OutcomeTick { get; init; }

    [JsonExtensionData]
    public IDictionary<string, JsonElement>? AdditiveFields { get; init; }
}

public sealed record ProposalResult(
    [property: JsonPropertyName("proposal")] ActionProposal Proposal,
    [property: JsonPropertyName("duplicate")] bool Duplicate)
{
    [JsonExtensionData]
    public IDictionary<string, JsonElement>? AdditiveFields { get; init; }
}

// Package protocol defines Rin's language-neutral JSON contract.
package protocol

import "encoding/json"

const Version = "rin.protocol/v1"

type Binding struct {
	GameID         string `json:"game_id"`
	ContentID      string `json:"content_id"`
	ContentVersion string `json:"content_version"`
	ContentHash    string `json:"content_hash"`
}

type Boundary struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	TriggerTags []string `json:"trigger_tags"`
	Response    string   `json:"response"`
}

type Goal struct {
	ID               string   `json:"id"`
	Description      string   `json:"description"`
	Motivation       string   `json:"motivation,omitempty"`
	Priority         int      `json:"priority"`
	PreferredActions []string `json:"preferred_actions,omitempty"`
	Progress         int      `json:"progress"`
	TargetProgress   int      `json:"target_progress"`
	Status           string   `json:"status"`
	// UpdatedTick is the latest game occurrence tick whose status or progress
	// has been merged into this goal. It prevents a late outcome from
	// overwriting a newer terminal status.
	UpdatedTick int64 `json:"updated_tick,omitempty"`
	// ProgressAccumulator preserves the unclamped sum of authoritative
	// progress deltas. Progress is its bounded projection, so late positive and
	// negative deltas produce the same value regardless of report order.
	ProgressAccumulator int64 `json:"progress_accumulator,omitempty"`
	// StatusExplicit distinguishes a game-supplied status from the automatic
	// active/completed projection of progress.
	StatusExplicit bool `json:"status_explicit,omitempty"`
	// StatusUpdatedTick and StatusSourceEventID order explicit game status
	// updates independently from progress-only updates.
	StatusUpdatedTick   int64  `json:"status_updated_tick,omitempty"`
	StatusSourceEventID string `json:"status_source_event_id,omitempty"`
}

type ActorSeed struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	DisplayName     string            `json:"display_name"`
	Traits          []string          `json:"traits,omitempty"`
	Boundaries      []Boundary        `json:"boundaries,omitempty"`
	Goals           []Goal            `json:"goals,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ThinkEveryTicks int64             `json:"think_every_ticks"`
	Enabled         bool              `json:"enabled"`
}

type Fact struct {
	SubjectID     string   `json:"subject_id"`
	Predicate     string   `json:"predicate"`
	Object        string   `json:"object"`
	Visibility    []string `json:"visibility,omitempty"`
	Confidence    int      `json:"confidence"`
	SourceEventID string   `json:"source_event_id,omitempty"`
	// ObservedTick records when the fact occurred in the authoritative game,
	// rather than when its report reached Rin.
	ObservedTick int64 `json:"observed_tick,omitempty"`
}

type Memory struct {
	ID               string   `json:"id"`
	EventID          string   `json:"event_id"`
	Tick             int64    `json:"tick"`
	Summary          string   `json:"summary"`
	Quote            string   `json:"quote,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Importance       int      `json:"importance"`
	CreatedRevision  uint64   `json:"created_revision"`
	RecallCount      int      `json:"recall_count"`
	LastRecalledTick int64    `json:"last_recalled_tick"`
}

// MemorySummary retains bounded, explainable context after detailed episodic
// memories are compacted. Source lists are intentionally bounded; the event
// log remains the complete audit record.
type MemorySummary struct {
	ID               string   `json:"id"`
	Level            int      `json:"level"`
	Summary          string   `json:"summary"`
	Tags             []string `json:"tags,omitempty"`
	SourceMemoryIDs  []string `json:"source_memory_ids,omitempty"`
	SourceEventIDs   []string `json:"source_event_ids,omitempty"`
	StartTick        int64    `json:"start_tick"`
	EndTick          int64    `json:"end_tick"`
	Importance       int      `json:"importance"`
	Reason           string   `json:"reason"`
	CreatedRevision  uint64   `json:"created_revision"`
	RecallCount      int      `json:"recall_count"`
	LastRecalledTick int64    `json:"last_recalled_tick"`
}

type BeliefClaim struct {
	Fact             Fact   `json:"fact"`
	ObservedRevision uint64 `json:"observed_revision"`
}

// BeliefSet preserves contradictory actor-local claims while Beliefs remains
// the compatibility projection of the currently selected claim.
type BeliefSet struct {
	SubjectID             string        `json:"subject_id"`
	Predicate             string        `json:"predicate"`
	Claims                []BeliefClaim `json:"claims"`
	SelectedSourceEventID string        `json:"selected_source_event_id"`
	Conflicted            bool          `json:"conflicted"`
}

type ActionSpec struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Description string            `json:"description"`
	TargetIDs   []string          `json:"target_ids,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
}

type ActionProposal struct {
	ID                   string     `json:"id"`
	SessionID            string     `json:"session_id"`
	RequestID            string     `json:"request_id"`
	ActorID              string     `json:"actor_id"`
	Tick                 int64      `json:"tick"`
	BasedOnRevision      uint64     `json:"based_on_revision"`
	BasedOnHeadHash      string     `json:"based_on_head_hash"`
	BasedOnWorldRevision uint64     `json:"based_on_world_revision,omitempty"`
	CreatedRevision      uint64     `json:"created_revision"`
	Action               ActionSpec `json:"action"`
	Stance               string     `json:"stance"`
	Summary              string     `json:"summary"`
	Rationale            string     `json:"rationale"`
	PolicySource         string     `json:"policy_source,omitempty"`
	RecalledMemoryIDs    []string   `json:"recalled_memory_ids,omitempty"`
	GoalID               string     `json:"goal_id,omitempty"`
	ProposedGoal         *Goal      `json:"proposed_goal,omitempty"`
	Status               string     `json:"status"`
	// OutcomeEventID and OutcomeTick are populated when the authoritative game
	// reports this proposal's result. They also make rejected outcome event IDs
	// discoverable and order accepted actions by occurrence rather than arrival.
	OutcomeEventID string `json:"outcome_event_id,omitempty"`
	OutcomeTick    int64  `json:"outcome_tick,omitempty"`
}

type ActorState struct {
	ActorSeed
	Memories        []Memory             `json:"memories,omitempty"`
	MemorySummaries []MemorySummary      `json:"memory_summaries,omitempty"`
	Beliefs         map[string]Fact      `json:"beliefs,omitempty"`
	BeliefSets      map[string]BeliefSet `json:"belief_sets,omitempty"`
	RecentActions   []ActionProposal     `json:"recent_actions,omitempty"`
	NextThinkTick   int64                `json:"next_think_tick"`
	Activity        *ActorActivity       `json:"activity,omitempty"`
}

type RequestReceipt struct {
	Kind        string `json:"kind"`
	EntityID    string `json:"entity_id,omitempty"`
	Revision    uint64 `json:"revision"`
	RequestHash string `json:"request_hash,omitempty"`
}

type SessionState struct {
	ProtocolVersion string                    `json:"protocol_version"`
	SessionID       string                    `json:"session_id"`
	Binding         Binding                   `json:"binding"`
	Seed            int64                     `json:"seed"`
	Features        []string                  `json:"features,omitempty"`
	Tick            int64                     `json:"tick"`
	Revision        uint64                    `json:"revision"`
	WorldRevision   uint64                    `json:"world_revision,omitempty"`
	HeadHash        string                    `json:"head_hash"`
	Actors          map[string]ActorState     `json:"actors"`
	Proposals       map[string]ActionProposal `json:"proposals,omitempty"`
	Arbitrations    []ArbitrationRecord       `json:"arbitrations,omitempty"`
	Receipts        map[string]RequestReceipt `json:"receipts,omitempty"`
}

type CreateSessionRequest struct {
	ProtocolVersion string      `json:"protocol_version"`
	RequestID       string      `json:"request_id"`
	SessionID       string      `json:"session_id"`
	Binding         Binding     `json:"binding"`
	Seed            int64       `json:"seed"`
	Features        []string    `json:"features,omitempty"`
	Actors          []ActorSeed `json:"actors"`
}

type ObserveRequest struct {
	ProtocolVersion string   `json:"protocol_version"`
	SessionID       string   `json:"session_id"`
	RequestID       string   `json:"request_id"`
	EventID         string   `json:"event_id"`
	Tick            int64    `json:"tick"`
	ObserverIDs     []string `json:"observer_ids"`
	Source          string   `json:"source"`
	Kind            string   `json:"kind"`
	Summary         string   `json:"summary"`
	Quote           string   `json:"quote,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	Importance      int      `json:"importance"`
	Facts           []Fact   `json:"facts,omitempty"`
}

type ProposeRequest struct {
	ProtocolVersion  string       `json:"protocol_version"`
	SessionID        string       `json:"session_id"`
	RequestID        string       `json:"request_id"`
	ActorID          string       `json:"actor_id"`
	Tick             int64        `json:"tick"`
	Intent           string       `json:"intent"`
	Tags             []string     `json:"tags,omitempty"`
	CandidateActions []ActionSpec `json:"candidate_actions"`
	CandidateGoals   []Goal       `json:"candidate_goals,omitempty"`
	Urgent           bool         `json:"urgent,omitempty"`
}

type GoalUpdate struct {
	GoalID        string `json:"goal_id"`
	ProgressDelta int    `json:"progress_delta"`
	Status        string `json:"status,omitempty"`
}

// CommitRequest reports the authoritative result after the game has applied or
// rejected a proposal. It does not authorize or execute the proposed action.
type CommitRequest struct {
	ProtocolVersion string       `json:"protocol_version"`
	SessionID       string       `json:"session_id"`
	RequestID       string       `json:"request_id"`
	ProposalID      string       `json:"proposal_id"`
	EventID         string       `json:"event_id"`
	Tick            int64        `json:"tick"`
	Accepted        bool         `json:"accepted"`
	Outcome         string       `json:"outcome"`
	Tags            []string     `json:"tags,omitempty"`
	Facts           []Fact       `json:"facts,omitempty"`
	GoalUpdates     []GoalUpdate `json:"goal_updates,omitempty"`
}

type SessionRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
}

type RestoreRequest struct {
	ProtocolVersion string   `json:"protocol_version"`
	SessionID       string   `json:"session_id"`
	RequestID       string   `json:"request_id"`
	Snapshot        Snapshot `json:"snapshot"`
}

type DueAgentsRequest struct {
	ProtocolVersion string   `json:"protocol_version"`
	SessionID       string   `json:"session_id"`
	Tick            int64    `json:"tick"`
	Limit           int      `json:"limit"`
	RegionIDs       []string `json:"region_ids,omitempty"`
}

type DueAgent struct {
	ActorID       string `json:"actor_id"`
	NextThinkTick int64  `json:"next_think_tick"`
	RegionID      string `json:"region_id,omitempty"`
}

type DueAgentsResponse struct {
	SessionID string     `json:"session_id"`
	Tick      int64      `json:"tick"`
	Agents    []DueAgent `json:"agents"`
}

type MutationResult struct {
	SessionID string `json:"session_id"`
	Revision  uint64 `json:"revision"`
	HeadHash  string `json:"head_hash"`
	Duplicate bool   `json:"duplicate"`
}

type ProposalResult struct {
	Proposal  ActionProposal `json:"proposal"`
	Duplicate bool           `json:"duplicate"`
}

type ProposalJobSubmission struct {
	ProtocolVersion string `json:"protocol_version"`
	JobID           string `json:"job_id"`
	Status          string `json:"status"`
	Duplicate       bool   `json:"duplicate"`
}

type ProposalJob struct {
	ProtocolVersion string          `json:"protocol_version"`
	JobID           string          `json:"job_id"`
	SessionID       string          `json:"session_id"`
	RequestID       string          `json:"request_id"`
	Status          string          `json:"status"`
	SubmittedAt     string          `json:"submitted_at"`
	StartedAt       string          `json:"started_at,omitempty"`
	FinishedAt      string          `json:"finished_at,omitempty"`
	Proposal        *ActionProposal `json:"proposal,omitempty"`
	Duplicate       bool            `json:"duplicate,omitempty"`
	Error           *ErrorDetail    `json:"error,omitempty"`
}

// GenerationMessage is the deliberately small prompt surface exposed to games.
// Provider selection, credentials, tools, and transport options stay inside Rin.
type GenerationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type GenerationRequest struct {
	ProtocolVersion string              `json:"protocol_version"`
	RequestID       string              `json:"request_id"`
	Kind            string              `json:"kind"`
	ContextHash     string              `json:"context_hash"`
	Messages        []GenerationMessage `json:"messages"`
	Temperature     float64             `json:"temperature"`
	MaxTokens       int                 `json:"max_tokens"`
	ResponseFormat  string              `json:"response_format"`
}

type GenerationResult struct {
	Content      string `json:"content"`
	Model        string `json:"model,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	PromptTokens int    `json:"prompt_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	TotalTokens  int    `json:"total_tokens,omitempty"`
	CacheHit     bool   `json:"cache_hit,omitempty"`
}

type GenerationJobSubmission struct {
	ProtocolVersion string `json:"protocol_version"`
	JobID           string `json:"job_id"`
	Status          string `json:"status"`
	Duplicate       bool   `json:"duplicate"`
}

type GenerationJob struct {
	ProtocolVersion string            `json:"protocol_version"`
	JobID           string            `json:"job_id"`
	RequestID       string            `json:"request_id"`
	Kind            string            `json:"kind"`
	ContextHash     string            `json:"context_hash"`
	Status          string            `json:"status"`
	SubmittedAt     string            `json:"submitted_at"`
	StartedAt       string            `json:"started_at,omitempty"`
	FinishedAt      string            `json:"finished_at,omitempty"`
	Result          *GenerationResult `json:"result,omitempty"`
	Duplicate       bool              `json:"duplicate,omitempty"`
	Error           *ErrorDetail      `json:"error,omitempty"`
}

type Snapshot struct {
	ProtocolVersion string       `json:"protocol_version"`
	StateHash       string       `json:"state_hash"`
	State           SessionState `json:"state"`
}

type EventRecord struct {
	Sequence   uint64          `json:"sequence"`
	Type       string          `json:"type"`
	RequestID  string          `json:"request_id"`
	PrevHash   string          `json:"prev_hash"`
	Hash       string          `json:"hash"`
	RecordedAt string          `json:"recorded_at"`
	Data       json.RawMessage `json:"data"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type APIResponse struct {
	OK    bool         `json:"ok"`
	Data  any          `json:"data,omitempty"`
	Error *ErrorDetail `json:"error,omitempty"`
}

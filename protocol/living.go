package protocol

type ActorActivity struct {
	RegionID        string `json:"region_id,omitempty"`
	State           string `json:"state"`
	Reason          string `json:"reason,omitempty"`
	UpdatedTick     int64  `json:"updated_tick"`
	UpdatedRevision uint64 `json:"updated_revision"`
}

type ActorActivityUpdate struct {
	ActorID  string `json:"actor_id"`
	RegionID string `json:"region_id,omitempty"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
}

type SetActorActivityRequest struct {
	ProtocolVersion string                `json:"protocol_version"`
	SessionID       string                `json:"session_id"`
	RequestID       string                `json:"request_id"`
	Tick            int64                 `json:"tick"`
	Updates         []ActorActivityUpdate `json:"updates"`
}

type ArbitrationDecision struct {
	ProposalID             string   `json:"proposal_id"`
	ActorID                string   `json:"actor_id"`
	Status                 string   `json:"status"`
	Reason                 string   `json:"reason"`
	ConflictingProposalIDs []string `json:"conflicting_proposal_ids,omitempty"`
}

type ArbitrationRecord struct {
	ID                   string                `json:"id"`
	RequestID            string                `json:"request_id"`
	Tick                 int64                 `json:"tick"`
	BasedOnWorldRevision uint64                `json:"based_on_world_revision"`
	CreatedRevision      uint64                `json:"created_revision"`
	Decisions            []ArbitrationDecision `json:"decisions"`
}

type ArbitrateRequest struct {
	ProtocolVersion    string   `json:"protocol_version"`
	SessionID          string   `json:"session_id"`
	RequestID          string   `json:"request_id"`
	Tick               int64    `json:"tick"`
	ProposalIDs        []string `json:"proposal_ids"`
	ExclusiveTargetIDs []string `json:"exclusive_target_ids,omitempty"`
}

type ArbitrationResult struct {
	Record    ArbitrationRecord `json:"record"`
	Duplicate bool              `json:"duplicate"`
}

type CommitItem struct {
	ProposalID  string       `json:"proposal_id"`
	EventID     string       `json:"event_id"`
	Accepted    bool         `json:"accepted"`
	Outcome     string       `json:"outcome,omitempty"`
	Tags        []string     `json:"tags,omitempty"`
	Facts       []Fact       `json:"facts,omitempty"`
	GoalUpdates []GoalUpdate `json:"goal_updates,omitempty"`
}

type BatchCommitRequest struct {
	ProtocolVersion string       `json:"protocol_version"`
	SessionID       string       `json:"session_id"`
	RequestID       string       `json:"request_id"`
	Tick            int64        `json:"tick"`
	Items           []CommitItem `json:"items"`
}

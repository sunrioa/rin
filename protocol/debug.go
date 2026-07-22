package protocol

// TimelineRequest selects a bounded page of redacted event metadata. The
// event payload is never returned by this endpoint.
type TimelineRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	AfterRevision   uint64 `json:"after_revision,omitempty"`
	Limit           int    `json:"limit"`
}

type TimelineEntry struct {
	Sequence   uint64   `json:"sequence"`
	Type       string   `json:"type"`
	RequestID  string   `json:"request_id"`
	RecordedAt string   `json:"recorded_at"`
	Hash       string   `json:"hash"`
	PrevHash   string   `json:"prev_hash,omitempty"`
	EntityIDs  []string `json:"entity_ids,omitempty"`
	ActorIDs   []string `json:"actor_ids,omitempty"`
	Status     string   `json:"status,omitempty"`
}

type TimelineResponse struct {
	SessionID         string          `json:"session_id"`
	CurrentRevision   uint64          `json:"current_revision"`
	Entries           []TimelineEntry `json:"entries"`
	NextAfterRevision uint64          `json:"next_after_revision"`
	HasMore           bool            `json:"has_more"`
}

type ReplayRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	Revision        uint64 `json:"revision"`
}

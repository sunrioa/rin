// Package runtime owns Rin's authoritative, deterministic agent state machine.
package runtime

import (
	"context"
	"errors"

	"github.com/sunrioa/rin/protocol"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrStale        = errors.New("stale proposal")
	ErrNotDue       = errors.New("agent is not due")
	ErrNoSafeAction = errors.New("no safe action")
	ErrCorruptLog   = errors.New("corrupt event log")
)

const (
	EventSessionCreated  = "session.created"
	EventObserved        = "observation.recorded"
	EventProposed        = "proposal.created"
	EventCommitted       = "action.committed"
	EventBatchCommitted  = "action.batch-committed"
	EventActivityUpdated = "actor.activity-updated"
	EventArbitrated      = "world.arbitrated"
	EventSessionRestored = "session.restored"

	// CheckpointFormatVersion identifies the durable wrapper used only for
	// runtime replay acceleration. It is intentionally distinct from the public
	// Snapshot wire format.
	CheckpointFormatVersion = "rin.checkpoint/v1"
	// ReducerProjectionVersion must change whenever replaying the same event log
	// can produce a different State or Identifier History projection.
	ReducerProjectionVersion = "rin.reducer-projection/v2"
)

type Store interface {
	// Operations for one session must be linearizable and Load must provide
	// read-after-write consistency with Create and Append. In particular, after
	// a failed Create, Load returning ErrNotFound is treated as proof that no
	// first event was written; after a failed Append, Load returning the prior
	// tail is treated as proof that the candidate event was not written. A
	// Store which cannot make either observation authoritative must return an
	// uncertainty error from Load rather than stale data. Eventually consistent
	// implementations do not satisfy this interface.
	// Create is an idempotent create-if-absent operation. Repeating an
	// identical first EventRecord, including the exact Data bytes, is a
	// successful durability confirmation; any different existing log must
	// return ErrConflict without mutation.
	Create(sessionID string, event protocol.EventRecord) error
	// Append must compare the event with the current tail. Repeating an
	// identical EventRecord, including the exact Data bytes, is an idempotent
	// success; a different event at the same sequence or an unexpected
	// previous hash must fail without mutation.
	// It must never accept a partial record as a valid event. If an underlying
	// write or rollback fails, Load must surface the incomplete tail as
	// ErrCorruptLog instead of silently replaying it.
	Append(sessionID string, event protocol.EventRecord) error
	Load(sessionID string) ([]protocol.EventRecord, error)
	ListSessions() ([]string, error)
	SaveSnapshot(sessionID string, snapshot protocol.Snapshot) error
}

// EventAnchor identifies an immutable point in one Session event chain.
type EventAnchor struct {
	Revision uint64 `json:"revision"`
	HeadHash string `json:"head_hash"`
}

// EventPage is a bounded, contiguous event range. HasMore is relative to the
// throughRevision supplied to RangeStore.LoadRange.
type EventPage struct {
	Events  []protocol.EventRecord `json:"events"`
	HasMore bool                   `json:"has_more"`
}

// RangeStore is an optional Store capability. LoadRange returns events in the
// interval (afterRevision, throughRevision], capped by limit. Implementations
// must return a contiguous, hash-verified prefix and must not return events
// newer than throughRevision. The base Store interface remains unchanged so
// existing third-party Stores retain source compatibility.
type RangeStore interface {
	Head(sessionID string) (EventAnchor, error)
	LoadRange(
		sessionID string,
		afterRevision uint64,
		throughRevision uint64,
		limit int,
	) (EventPage, error)
}

// Checkpoint is a derived replay cache, not an exported or imported Snapshot.
// Its Snapshot may exceed the public inline transport ceiling. Checksum detects
// accidental corruption; it is not authentication or provenance proof.
type Checkpoint struct {
	FormatVersion     string            `json:"format_version"`
	ProjectionVersion string            `json:"projection_version"`
	SessionID         string            `json:"session_id"`
	Revision          uint64            `json:"revision"`
	HeadHash          string            `json:"head_hash"`
	LineageEpoch      uint64            `json:"lineage_epoch"`
	Snapshot          protocol.Snapshot `json:"snapshot"`
	Checksum          string            `json:"checksum"`
}

// CheckpointStore is an optional Store capability. LoadCheckpoint returns the
// newest available checkpoint at or before atOrBeforeRevision. Returning
// ErrNotFound means no eligible checkpoint is available. Implementations may
// retain multiple generations so the Engine can fall back after corruption.
//
// Runtime may call SaveCheckpoint in a background worker concurrently with
// Append, Load, Head, or LoadRange for the same Session. Implementations must
// be concurrency-safe and must keep this derived-cache I/O from changing or
// indefinitely locking authoritative event operations. Within one Engine,
// Runtime bounds work to one worker and one latest pending capture per managed
// Session. Multiple Engines sharing a Store can each have such a worker.
// Runtime has no Close/drain contract for a Store implementation that blocks
// SaveCheckpoint forever.
type CheckpointStore interface {
	LoadCheckpoint(sessionID string, atOrBeforeRevision uint64) (Checkpoint, error)
	SaveCheckpoint(sessionID string, checkpoint Checkpoint) error
}

type PolicyContext struct {
	State   protocol.SessionState
	Actor   protocol.ActorState
	Request protocol.ProposeRequest
}

type ProposalDraft struct {
	ActionID string
	Stance   string
	// Summary and Rationale are retained for source compatibility with custom
	// policies, but the engine never publishes them. Player-facing text is
	// rebuilt from the selected game-authored action and fixed templates.
	Summary           string
	Rationale         string
	PolicySource      string
	RecalledMemoryIDs []string
	GoalID            string
	BoundaryID        string
}

// Policy proposes an allowed game action. Implementations may be deterministic,
// model-backed, or scripted, but the runtime validates every returned field.
type Policy interface {
	Propose(context.Context, PolicyContext) (ProposalDraft, error)
}

type CodedError struct {
	Code    string
	Message string
	Field   string
	Cause   error
}

func (e *CodedError) Error() string { return e.Message }
func (e *CodedError) Unwrap() error { return e.Cause }

func NewError(code, message string, cause error) error {
	return &CodedError{Code: code, Message: message, Cause: cause}
}

func NewFieldError(code, message, field string, cause error) error {
	return &CodedError{Code: code, Message: message, Field: field, Cause: cause}
}

func ErrorCode(err error) string {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return "internal_error"
}

func ErrorField(err error) string {
	var coded *CodedError
	if errors.As(err, &coded) && coded.Field != "" {
		return coded.Field
	}
	var validation *protocol.ValidationError
	if errors.As(err, &validation) {
		return validation.Field
	}
	return ""
}

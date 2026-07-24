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

type PolicyContext struct {
	State   protocol.SessionState
	Actor   protocol.ActorState
	Request protocol.ProposeRequest
}

type ProposalDraft struct {
	ActionID          string
	Stance            string
	Summary           string
	Rationale         string
	PolicySource      string
	RecalledMemoryIDs []string
	GoalID            string
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

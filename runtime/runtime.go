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
	EventSessionRestored = "session.restored"
)

type Store interface {
	Create(sessionID string, event protocol.EventRecord) error
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

package hostkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/protocol"
)

const (
	// StateVersion is the only persisted HostKit workflow shape supported by
	// this pre-1.0 release.
	StateVersion     = 1
	maxActions       = 1024
	maxOutboxEntries = 1024
)

var (
	// ErrConcurrentUpdate reports a failed optimistic state write.
	ErrConcurrentUpdate = errors.New("host workflow state changed concurrently")
	// ErrPendingMissing reports that no decision can be resumed or settled.
	ErrPendingMissing = errors.New("no pending decision")
	// ErrPendingExists reports that one Host slot already owns pending work.
	ErrPendingExists = errors.New("a pending decision already exists")
	// ErrOutboxPending requires exact reports to drain before new work starts.
	ErrOutboxPending = errors.New("Outcome Outbox must drain before a new decision")
	// ErrStaleEpoch reports work bound to a replaced Host, World, or Timeline.
	ErrStaleEpoch = errors.New("workflow belongs to a stale Host epoch")
)

// PendingDecision is the durable request identity retained before network I/O.
type PendingDecision struct {
	OperationID string                  `json:"operation_id"`
	Request     protocol.ProposeRequest `json:"request"`
	JobID       string                  `json:"job_id,omitempty"`
}

// ActionRecord is the latest host-owned lifecycle state of one invocation.
type ActionRecord struct {
	ProposalID        string                `json:"proposal_id"`
	ProposalRequestID string                `json:"proposal_request_id"`
	Invocation        host.ActionInvocation `json:"invocation"`
	Run               host.ActionRun        `json:"run"`
	Outcome           *host.ActionOutcome   `json:"outcome,omitempty"`
}

// OutboxEntry retains one exact Action Report until Rin acknowledges it.
type OutboxEntry struct {
	ID      string                       `json:"id"`
	Request protocol.ReportActionRequest `json:"request"`
}

// WorkflowState contains DTOs only and is safe to serialize in a game save.
type WorkflowState struct {
	Version  int              `json:"version"`
	Revision uint64           `json:"revision"`
	Pending  *PendingDecision `json:"pending,omitempty"`
	Actions  []ActionRecord   `json:"actions,omitempty"`
	Outbox   []OutboxEntry    `json:"outbox,omitempty"`
}

// EmptyState constructs a valid empty workflow state.
func EmptyState() WorkflowState {
	return WorkflowState{Version: StateVersion}
}

// Clone returns a deep DTO copy suitable for optimistic mutation.
func (state WorkflowState) Clone() (WorkflowState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return WorkflowState{}, fmt.Errorf("encode workflow state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cloned WorkflowState
	if err := decoder.Decode(&cloned); err != nil {
		return WorkflowState{}, fmt.Errorf("decode workflow state: %w", err)
	}
	return cloned, nil
}

// Validate rejects corrupt, unbounded, duplicated, or invalid persisted state.
func (state WorkflowState) Validate() error {
	if state.Version != StateVersion {
		return fmt.Errorf("workflow state version must equal %d", StateVersion)
	}
	if len(state.Actions) > maxActions {
		return fmt.Errorf("workflow state contains more than %d actions", maxActions)
	}
	if len(state.Outbox) > maxOutboxEntries {
		return fmt.Errorf("workflow state contains more than %d Outbox entries", maxOutboxEntries)
	}
	if state.Pending != nil {
		if err := protocol.ValidateIdentifier(
			"pending.operation_id", state.Pending.OperationID,
		); err != nil {
			return err
		}
		if state.Pending.JobID != "" {
			if err := protocol.ValidateIdentifier(
				"pending.job_id", state.Pending.JobID,
			); err != nil {
				return err
			}
		}
		if err := protocol.ValidatePropose(state.Pending.Request); err != nil {
			return fmt.Errorf("validate pending decision: %w", err)
		}
	}
	operations := make(map[string]struct{}, len(state.Actions))
	for index, action := range state.Actions {
		if err := protocol.ValidateIdentifier(
			fmt.Sprintf("actions[%d].proposal_id", index), action.ProposalID,
		); err != nil {
			return err
		}
		if err := protocol.ValidateIdentifier(
			fmt.Sprintf("actions[%d].proposal_request_id", index),
			action.ProposalRequestID,
		); err != nil {
			return err
		}
		if err := host.ValidateActionInvocation(action.Invocation); err != nil {
			return fmt.Errorf("validate actions[%d].invocation: %w", index, err)
		}
		if err := host.ValidateActionRun(action.Run); err != nil {
			return fmt.Errorf("validate actions[%d].run: %w", index, err)
		}
		if action.Invocation.OperationID != action.Run.OperationID {
			return fmt.Errorf("actions[%d] operation IDs do not match", index)
		}
		if action.Outcome != nil {
			if err := host.ValidateActionOutcome(*action.Outcome); err != nil {
				return fmt.Errorf("validate actions[%d].outcome: %w", index, err)
			}
			if action.Outcome.OperationID != action.Invocation.OperationID {
				return fmt.Errorf("actions[%d] outcome operation ID does not match", index)
			}
		}
		if terminal(action.Run.Status) != (action.Outcome != nil) {
			return fmt.Errorf(
				"actions[%d] terminal Run and Outcome presence do not match", index)
		}
		if _, exists := operations[action.Invocation.OperationID]; exists {
			return fmt.Errorf("operation %q is duplicated", action.Invocation.OperationID)
		}
		operations[action.Invocation.OperationID] = struct{}{}
	}
	outboxIDs := make(map[string]struct{}, len(state.Outbox))
	for index, entry := range state.Outbox {
		if err := protocol.ValidateIdentifier(
			fmt.Sprintf("outbox[%d].id", index), entry.ID,
		); err != nil {
			return err
		}
		if _, exists := outboxIDs[entry.ID]; exists {
			return fmt.Errorf("Outbox ID %q is duplicated", entry.ID)
		}
		outboxIDs[entry.ID] = struct{}{}
		if err := protocol.ValidateReportAction(entry.Request); err != nil {
			return fmt.Errorf("validate outbox[%d]: %w", index, err)
		}
	}
	return nil
}

func terminal(status host.ActionRunStatus) bool {
	switch status {
	case host.ActionSucceeded, host.ActionFailed, host.ActionCancelled,
		host.ActionInterrupted, host.ActionStale:
		return true
	default:
		return false
	}
}

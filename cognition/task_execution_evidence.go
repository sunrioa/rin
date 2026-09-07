package cognition

import (
	"errors"
	"slices"
)

const TaskExecutionEvidenceVersion = "rin.task-execution/v1"

// TaskExecutionEvidence is updated by the runtime in the same task CAS as each
// state transition. It survives History retention; it is never model-authored.
// Complete is false for legacy tasks whose earlier events are unavailable.
type TaskExecutionEvidence struct {
	ContractVersion          string   `json:"contract_version"`
	Complete                 bool     `json:"complete"`
	EventSequence            uint64   `json:"event_sequence"`
	SelectedActionCount      uint32   `json:"selected_action_count"`
	TerminalOperationCount   uint32   `json:"terminal_operation_count"`
	SuccessfulOperationCount uint32   `json:"successful_operation_count"`
	RejectedActionCount      uint32   `json:"rejected_action_count"`
	StartedMacroCount        uint32   `json:"started_macro_count"`
	TerminalMacroCount       uint32   `json:"terminal_macro_count"`
	TaskCompletedCount       uint32   `json:"task_completed_count"`
	SelectedCapabilities     []string `json:"selected_capabilities"`
	LastOperationID          string   `json:"last_operation_id,omitempty"`
}

func cloneTaskExecutionEvidence(value *TaskExecutionEvidence) *TaskExecutionEvidence {
	if value == nil {
		return nil
	}
	copy := *value
	copy.SelectedCapabilities = append([]string{}, value.SelectedCapabilities...)
	return &copy
}

func executionEvidenceFromHistory(task TaskSession) *TaskExecutionEvidence {
	evidence := &TaskExecutionEvidence{
		ContractVersion:      TaskExecutionEvidenceVersion,
		Complete:             uint64(len(task.History)) == task.EventSequence,
		SelectedCapabilities: []string{},
	}
	for i, event := range task.History {
		if event.Sequence != uint64(i+1) {
			evidence.Complete = false
		}
		evidence.record(event)
	}
	evidence.EventSequence = task.EventSequence
	return evidence
}

func (evidence *TaskExecutionEvidence) record(event TaskEvent) {
	evidence.EventSequence = event.Sequence
	switch event.Kind {
	case "action.selected":
		evidence.SelectedActionCount++
		if !slices.Contains(evidence.SelectedCapabilities, event.Code) {
			evidence.SelectedCapabilities = append(evidence.SelectedCapabilities, event.Code)
			slices.Sort(evidence.SelectedCapabilities)
		}
	case "action.rejected", "action.invalidated":
		// Both discard a selected intent before an operation was submitted.
		evidence.RejectedActionCount++
	case "macro.started":
		evidence.StartedMacroCount++
	case "operation.terminal", "macro.terminal":
		evidence.TerminalOperationCount++
		evidence.LastOperationID = event.OperationID
		if event.Kind == "macro.terminal" {
			evidence.TerminalMacroCount++
		}
		if event.Code == "succeeded" {
			evidence.SuccessfulOperationCount++
		}
	case "task.completed":
		evidence.TaskCompletedCount++
	}
}

func validateTaskExecutionEvidence(task TaskSession) error {
	e := task.ExecutionEvidence
	if e == nil {
		// Existing snapshots remain readable without fabricating lost evidence.
		return nil
	}
	if e.ContractVersion != TaskExecutionEvidenceVersion || e.EventSequence != task.EventSequence ||
		e.SelectedActionCount > task.ActionCount ||
		e.SuccessfulOperationCount > e.TerminalOperationCount ||
		e.TerminalOperationCount > task.ActionCount || e.RejectedActionCount > task.ActionCount ||
		uint64(e.TerminalOperationCount)+uint64(e.RejectedActionCount) > uint64(task.ActionCount) ||
		e.StartedMacroCount > task.ActionCount || e.TerminalMacroCount > e.TerminalOperationCount ||
		e.TaskCompletedCount > 1 || len(e.SelectedCapabilities) > 128 {
		return errors.New("invalid task execution evidence")
	}
	for i, capability := range e.SelectedCapabilities {
		if validateProviderID("execution_evidence.selected_capabilities", capability) != nil ||
			!taskAllowsCapability(task, capability) ||
			(i > 0 && e.SelectedCapabilities[i-1] >= capability) {
			return errors.New("invalid execution evidence capability scope")
		}
	}
	if e.LastOperationID != "" && validateProviderID("execution_evidence.last_operation_id", e.LastOperationID) != nil {
		return errors.New("invalid execution evidence operation id")
	}
	if e.Complete && (e.SelectedActionCount != task.ActionCount ||
		e.TerminalMacroCount > e.StartedMacroCount ||
		(e.TerminalOperationCount > 0 && e.LastOperationID == "") ||
		(e.SelectedActionCount > 0 && len(e.SelectedCapabilities) == 0)) {
		return errors.New("task execution evidence is inconsistent with its state")
	}
	if e.Complete && task.Status == TaskCompleted &&
		(e.TaskCompletedCount != 1 || e.StartedMacroCount != e.TerminalMacroCount ||
			uint64(e.TerminalOperationCount)+uint64(e.RejectedActionCount) != uint64(task.ActionCount)) {
		return errors.New("completed task retains unresolved execution evidence")
	}
	return nil
}

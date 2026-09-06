package cognition

import (
	"errors"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

// LookaheadOptions enables one speculative successor per ordinary operation.
// Providers without LookaheadProvider keep the serial execution path.
type LookaheadOptions struct {
	Disabled       bool   `json:"disabled,omitempty"`
	MaxConcurrent  uint32 `json:"max_concurrent,omitempty"`
	TimeoutMillis  uint32 `json:"timeout_millis,omitempty"`
	DraftTTLMillis uint32 `json:"draft_ttl_millis,omitempty"`
}

func NormalizeLookaheadOptions(options *LookaheadOptions) (LookaheadOptions, error) {
	var result LookaheadOptions
	if options != nil {
		result = *options
	}
	if result.MaxConcurrent == 0 {
		result.MaxConcurrent = 2
	}
	if result.TimeoutMillis == 0 {
		result.TimeoutMillis = 10_000
	}
	if result.DraftTTLMillis == 0 {
		result.DraftTTLMillis = 60_000
	}
	if result.MaxConcurrent > 32 || result.TimeoutMillis < 100 || result.TimeoutMillis > 60_000 ||
		result.DraftTTLMillis < result.TimeoutMillis || result.DraftTTLMillis > 300_000 {
		return LookaheadOptions{}, errors.New("invalid lookahead concurrency, timeout, or TTL")
	}
	return result, nil
}

// TaskLookaheadState persists accounting and the last attempted operation only.
// Candidate actions are process-local and are discarded after a restart.
type TaskLookaheadState struct {
	OperationID    string `json:"operation_id"`
	Status         string `json:"status"`
	Code           string `json:"code,omitempty"`
	ReservedTokens uint64 `json:"reserved_tokens,omitempty"`
	Calls          uint32 `json:"calls"`
	Adopted        uint32 `json:"adopted"`
	Discarded      uint32 `json:"discarded"`
}

func validateTaskLookahead(task TaskSession) error {
	state := task.Lookahead
	if state == nil {
		return nil
	}
	if err := validateMemoryOpaqueID("lookahead.operation_id", state.OperationID); err != nil {
		return err
	}
	switch state.Status {
	case "preparing", "running", "ready", "adopted", "discarded":
	default:
		return errors.New("invalid task lookahead status")
	}
	if state.ReservedTokens > maxProviderWireInteger || state.Calls > task.ModelCalls ||
		uint64(state.Adopted)+uint64(state.Discarded) > uint64(state.Calls) ||
		(state.ReservedTokens > 0 && state.Status != "running" && state.Status != "discarded") {
		return errors.New("invalid task lookahead accounting")
	}
	return validateProviderText("lookahead.code", state.Code, 128, false)
}

// Only caller intent, attention, and execution authority invalidate a draft.
// A result advancing Step, observation sequence, or plan revision is expected.
func lookaheadTaskIdentity(task TaskSession) string {
	return digestJSON(struct {
		TaskID, SessionID, HostID, WorldID, ActorID, ControllerID, Goal, PlanID, MacroID, LeaseID string
		CreatedAt                                                                                 int64
		Epoch                                                                                     host.Epoch
		Tags, AllowedCapabilities, SeenSignalIDs                                                  []string
		Signals                                                                                   []TaskSignal
		Completion                                                                                TaskCompletionPolicy
		PlanningMode                                                                              taskstate.PlanningMode
	}{task.TaskID, task.SessionID, task.HostID, task.WorldID, task.ActorID, task.ControllerID,
		task.Goal, task.PlanID, task.MacroOperationID, task.ControllerLease.LeaseID,
		task.CreatedAtUnixMillis, task.ControllerLease.Epoch, task.Tags, task.AllowedCapabilities,
		task.SeenSignalIDs, task.PendingSignals, task.Completion, task.PlanningMode})
}

func lookaheadPlanIdentity(plan *taskstate.PlanState) string {
	if plan == nil {
		return ""
	}
	steps := make([]taskstate.StepDraft, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		steps = append(steps, taskstate.StepDraft{StepID: step.StepID, Title: step.Title, Objective: step.Objective,
			SuccessConditions: step.SuccessConditions, CapabilityHints: step.CapabilityHints, MaxAttempts: step.MaxAttempts})
	}
	return digestJSON(struct {
		PlanID, Goal string
		Epoch        host.Epoch
		Replans      uint32
		Steps        []taskstate.StepDraft
		Conditions   []taskstate.PlanCondition
	}{plan.PlanID, plan.Goal, plan.BasedOnEpoch, plan.ReplanCount, steps, plan.SuccessConditions})
}

func lookaheadDuration(milliseconds uint32) time.Duration {
	return time.Duration(milliseconds) * time.Millisecond
}

func lookaheadDiscardState(task *TaskSession, code string) {
	state := task.Lookahead
	if state == nil || state.Status == "adopted" || state.Status == "discarded" {
		return
	}
	if state.Status != "preparing" {
		state.Discarded++
	}
	state.Status, state.Code = "discarded", code
}

package cognition

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

type TaskCompletionMode string

const (
	CompletionModel    TaskCompletionMode = "model-declared"
	CompletionEvidence TaskCompletionMode = "host-evidence"
	CompletionHuman    TaskCompletionMode = "human-confirmation"
)

// TaskCompletionPolicy is caller-authored acceptance criteria, independent of
// planning. A model can request completion but cannot change these criteria.
type TaskCompletionPolicy struct {
	Mode       TaskCompletionMode        `json:"mode"`
	Conditions []taskstate.PlanCondition `json:"conditions,omitempty"`
}

type TaskCompletionEvidence struct {
	ConditionID         string                 `json:"condition_id"`
	Kind                taskstate.EvidenceKind `json:"kind"`
	Epoch               host.Epoch             `json:"epoch"`
	ObservationSequence uint64                 `json:"observation_sequence"`
	OperationID         string                 `json:"operation_id,omitempty"`
}

func normalizeTaskCompletion(policy TaskCompletionPolicy) (TaskCompletionPolicy, error) {
	if policy.Mode == "" {
		policy.Mode = CompletionModel
	}
	switch policy.Mode {
	case CompletionModel, CompletionHuman:
		if len(policy.Conditions) != 0 {
			return policy, errors.New("only host-evidence completion accepts conditions")
		}
	case CompletionEvidence:
		if len(policy.Conditions) == 0 || len(policy.Conditions) > 16 {
			return policy, errors.New("host-evidence completion requires 1 to 16 conditions")
		}
		if err := taskstate.ValidateCompletionConditions(policy.Conditions); err != nil {
			return policy, err
		}
	default:
		return policy, errors.New("unsupported task completion mode")
	}
	return cloneTaskCompletion(policy), nil
}

func cloneTaskCompletion(policy TaskCompletionPolicy) TaskCompletionPolicy {
	policy.Conditions = append([]taskstate.PlanCondition(nil), policy.Conditions...)
	for i := range policy.Conditions {
		if policy.Conditions[i].Capability != nil {
			value := *policy.Conditions[i].Capability
			policy.Conditions[i].Capability = &value
		}
	}
	return policy
}

func validateTaskCompletionEvidence(task TaskSession) error {
	if len(task.CompletionEvidence) > len(task.Completion.Conditions) {
		return errors.New("too many task completion evidence records")
	}
	seen := make(map[string]bool)
	for _, e := range task.CompletionEvidence {
		i := slices.IndexFunc(task.Completion.Conditions, func(c taskstate.PlanCondition) bool { return c.ConditionID == e.ConditionID && c.Kind == e.Kind })
		if i < 0 || seen[e.ConditionID] {
			return errors.New("completion evidence has no unique matching condition")
		}
		seen[e.ConditionID] = true
		if err := e.Epoch.Validate("completion_evidence.epoch"); err != nil {
			return err
		}
		if e.Epoch.WorldID != task.WorldID || e.ObservationSequence == 0 || e.ObservationSequence > maxProviderWireInteger {
			return errors.New("invalid completion evidence scope or sequence")
		}
		if e.Kind == taskstate.EvidenceOperationOutcome {
			if err := validateProviderID("completion_evidence.operation_id", e.OperationID); err != nil {
				return err
			}
		} else if e.OperationID != "" {
			return errors.New("observation completion evidence cannot carry operation_id")
		}
	}
	return nil
}

func refreshCompletionFacts(task *TaskSession, observation host.ObservationEnvelope) {
	if task.Completion.Mode != CompletionEvidence {
		return
	}
	// Observation predicates must hold together in the current snapshot. Older
	// operation outcomes remain evidence only within the same Host epoch.
	task.CompletionEvidence = slices.DeleteFunc(task.CompletionEvidence, func(e TaskCompletionEvidence) bool {
		return e.Kind == taskstate.EvidenceObservationFact || e.Epoch != observation.Epoch
	})
	for _, condition := range task.Completion.Conditions {
		if condition.Kind != taskstate.EvidenceObservationFact {
			continue
		}
		for _, fact := range observation.Facts {
			if taskstate.ObservationConditionMatches(condition, fact) {
				task.CompletionEvidence = append(task.CompletionEvidence, TaskCompletionEvidence{ConditionID: condition.ConditionID,
					Kind: condition.Kind, Epoch: observation.Epoch, ObservationSequence: observation.Sequence})
				break
			}
		}
	}
}

func recordCompletionOutcome(task *TaskSession, view controlplane.OperationView) {
	if task.Completion.Mode != CompletionEvidence || !view.ExecutionConfirmed || view.Outcome == nil ||
		view.Outcome.Status != host.ActionSucceeded || view.Outcome.Epoch != task.ControllerLease.Epoch ||
		(view.OperationID != task.PendingOperationID && view.OperationID != task.MacroOperationID) {
		return
	}
	request := view.ActionRequest
	if request == nil {
		request = task.PendingAction
	}
	if request == nil || request.TaskID != task.TaskID {
		return
	}
	for _, condition := range task.Completion.Conditions {
		if condition.Kind != taskstate.EvidenceOperationOutcome || condition.Capability == nil || *condition.Capability != request.Capability {
			continue
		}
		task.CompletionEvidence = slices.DeleteFunc(task.CompletionEvidence, func(e TaskCompletionEvidence) bool { return e.ConditionID == condition.ConditionID })
		task.CompletionEvidence = append(task.CompletionEvidence, TaskCompletionEvidence{ConditionID: condition.ConditionID,
			Kind: condition.Kind, Epoch: view.Outcome.Epoch, ObservationSequence: view.Outcome.WorldSeq, OperationID: view.OperationID})
	}
}

func taskCompletionSatisfied(task TaskSession, epoch host.Epoch) bool {
	if task.Completion.Mode != CompletionEvidence || len(task.Completion.Conditions) == 0 {
		return false
	}
	for _, condition := range task.Completion.Conditions {
		if !slices.ContainsFunc(task.CompletionEvidence, func(e TaskCompletionEvidence) bool {
			return e.ConditionID == condition.ConditionID && e.Kind == condition.Kind && e.Epoch == epoch
		}) {
			return false
		}
	}
	return true
}

func (runtime *AgentRuntime) finishCompletedTask(ctx context.Context, task TaskSession, code, summary string) (TaskSession, error) {
	task.Status = TaskCompleted
	task.PauseCode = ""
	task.CompletionRequested = false
	appendTaskEvent(&task, TaskEvent{Kind: "task.completed", Step: task.Step, Code: code, Summary: summary, AtUnixMillis: runtime.now().UnixMilli()})
	saved, err := runtime.saveTask(ctx, task)
	if err == nil {
		runtime.releaseController(saved)
	}
	return saved, err
}

// ConfirmTaskCompletion accepts only the exact persisted human-review request.
// It cannot complete an active action, override a Plan, or revive cancellation.
func (runtime *AgentRuntime) ConfirmTaskCompletion(ctx context.Context, taskID string, expectedRevision uint64) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	if err := validateTaskID(taskID); err != nil {
		return TaskSession{}, err
	}
	if expectedRevision == 0 || expectedRevision > maxProviderWireInteger {
		return TaskSession{}, errors.New("completion confirmation requires expected_revision")
	}
	lock := runtime.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	task, err := runtime.tasks.Load(ctx, taskID)
	if err != nil {
		return task, err
	}
	if task.Revision != expectedRevision {
		return task, ErrTaskRevisionConflict
	}
	if task.Completion.Mode != CompletionHuman || !task.CompletionRequested || task.Status != TaskPaused ||
		task.PauseCode != "completion.confirmation-required" || task.CancelRequested || task.PendingAction != nil || task.MacroOperationID != "" {
		return task, fmt.Errorf("%w: task is not awaiting completion confirmation", ErrProviderConflict)
	}
	plan, task, err := runtime.loadTaskPlan(ctx, task)
	if err != nil {
		return task, err
	}
	if plan != nil && plan.Status != taskstate.PlanCompleted {
		return task, fmt.Errorf("%w: plan is not complete", ErrProviderConflict)
	}
	task, err = runtime.finishCompletedTask(ctx, task, "human-confirmed", "The caller accepted the task result.")
	if err != nil {
		return task, err
	}
	return runtime.maybeLearnSkill(ctx, task)
}

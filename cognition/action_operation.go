package cognition

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

func (runtime *AgentRuntime) advancePendingAction(
	ctx context.Context,
	task TaskSession,
) (TaskSession, bool, error) {
	if len(task.PendingMemories) != 0 {
		var warning bool
		if runtime.memory == nil {
			warning = true
		} else {
			for _, record := range task.PendingMemories {
				if _, err := runtime.memory.Append(ctx, record); err != nil {
					warning = true
				}
			}
		}
		task.PendingMemories = nil
		if warning {
			appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
		}
		var err error
		task, err = runtime.saveTask(ctx, task)
		if err != nil {
			return task, false, err
		}
	}
	if task.PendingOperationID == "" {
		action := controlplane.SubmitActionInput{
			HostID: task.HostID, WorldID: task.WorldID, Request: *task.PendingAction,
			ParentOperationID: task.MacroOperationID,
		}
		var view controlplane.OperationView
		var err error
		if task.PendingAction.PlanStep == nil {
			view, err = runtime.control.SubmitAction(ctx, runtime.principal, action)
		} else if runtime.plans == nil {
			err = errors.New("task plan coordinator is unavailable")
		} else {
			plan, planErr := runtime.plans.GetPlan(ctx, task.PendingAction.PlanStep.PlanID)
			if planErr != nil {
				err = planErr
			} else {
				view, err = runtime.plans.SubmitStepAction(ctx, taskstate.SubmitStepActionInput{
					Action: action,
					ConditionIDs: taskstate.OperationConditionIDs(
						plan, task.PendingAction.Capability,
					),
				})
			}
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, controlplane.ErrUnavailable) || errors.Is(err, controlplane.ErrPersistence) {
				paused, pauseErr := runtime.pauseTask(ctx, task, "action.submit-unavailable", err)
				return paused, false, pauseErr
			}
			if errors.Is(err, controlplane.ErrStale) || errors.Is(err, controlplane.ErrLeaseExpired) ||
				errors.Is(err, controlplane.ErrForbidden) || errors.Is(err, taskstate.ErrForbidden) {
				clearPendingTaskAction(&task)
				code := "controller.unavailable"
				if errors.Is(err, controlplane.ErrStale) {
					code = "plan.epoch-invalidated"
				} else if errors.Is(err, controlplane.ErrForbidden) || errors.Is(err, taskstate.ErrForbidden) {
					code = "controller.contended"
				}
				appendTaskEvent(&task, TaskEvent{
					Kind: "action.invalidated", Step: task.Step,
					Code:         actionSubmissionRejectionCode(err),
					AtUnixMillis: runtime.now().UnixMilli(),
				})
				paused, pauseErr := runtime.pauseTask(ctx, task, code, err)
				return paused, false, pauseErr
			}
			if errors.Is(err, controlplane.ErrInvalid) {
				clearPendingTaskAction(&task)
				task.Step++
				appendTaskEvent(&task, TaskEvent{
					Kind: "action.rejected", Step: task.Step,
					Code:         actionSubmissionRejectionCode(err),
					AtUnixMillis: runtime.now().UnixMilli(),
				})
				saved, saveErr := runtime.saveTask(ctx, task)
				return saved, saveErr == nil, errors.Join(err, saveErr)
			}
			failed, failErr := runtime.failTask(ctx, task, "action.submit-conflict", err)
			return failed, false, failErr
		}
		task.PendingOperationID = view.OperationID
		if view.Status == controlplane.OperationAwaitingConfirmation {
			task.Status = TaskWaitingConfirmation
		}
		appendTaskEvent(&task, operationTimelineEvent(
			task, "operation.submitted", view, runtime.now().UnixMilli(),
		))
		saved, saveErr := runtime.saveTask(ctx, task)
		return saved, saveErr == nil, saveErr
	}
	cancelling := task.Status == TaskCancelling
	var view controlplane.OperationView
	var err error
	if cancelling {
		view, err = runtime.control.CancelOperation(runtime.principal, task.PendingOperationID)
	} else {
		view, err = runtime.control.GetOperation(runtime.principal, task.PendingOperationID)
	}
	if err != nil {
		if cancelling {
			return task, false, err
		}
		paused, pauseErr := runtime.pauseTask(ctx, task, "operation.unavailable", err)
		return paused, false, pauseErr
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "operation.unknown", view)
	}
	if task.Status == TaskWaitingConfirmation &&
		view.Status != controlplane.OperationAwaitingConfirmation {
		task.Status = TaskActive
		task.PauseCode = ""
		var saveErr error
		task, saveErr = runtime.saveTask(ctx, task)
		if saveErr != nil {
			return task, false, saveErr
		}
	}
	if task.PendingActionMacro && macroOperationStarted(view) {
		return runtime.activatePendingMacro(ctx, task, view)
	}
	if !view.Terminal && view.Status != controlplane.OperationAwaitingConfirmation {
		update, waitErr := runtime.control.WaitOperation(ctx, runtime.principal, controlplane.WaitOperationInput{
			OperationID: view.OperationID, AfterCursor: view.Cursor,
			WaitMillis: runtime.operationWaitMillis,
		})
		if waitErr != nil {
			return task, false, waitErr
		}
		view = update.Operation
		if !update.Changed && !view.Terminal {
			return task, false, nil
		}
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "operation.unknown", view)
	}
	if view.Status == controlplane.OperationAwaitingConfirmation {
		if task.Status != TaskWaitingConfirmation {
			task.Status = TaskWaitingConfirmation
			saved, saveErr := runtime.saveTask(ctx, task)
			return saved, false, saveErr
		}
		return task, false, nil
	}
	if task.PendingActionMacro && macroOperationStarted(view) {
		return runtime.activatePendingMacro(ctx, task, view)
	}
	if !view.Terminal {
		if task.Status == TaskWaitingConfirmation {
			task.Status = TaskActive
			saved, saveErr := runtime.saveTask(ctx, task)
			return saved, saveErr == nil, saveErr
		}
		return task, false, nil
	}
	if operationOutcomeIsUnknown(view) {
		return runtime.recordUnknownOperation(ctx, task, "operation.unknown", view)
	}
	warning := false
	if view.Outcome != nil && !runtime.outcomesRecordedByControl {
		warning = runtime.appendOutcomeMemory(ctx, task, view, "outcome")
	}
	if cancelling && task.MacroOperationID == "" {
		task.Status = TaskCancelled
	} else if cancelling {
		task.Status = TaskCancelling
	} else {
		task.Status = TaskActive
	}
	task.PauseCode = ""
	result, resultErr := operationResult(task, view)
	if resultErr != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "operation.output-invalid", resultErr)
		return paused, false, pauseErr
	}
	task.LastOperationResult = result
	clearPendingTaskAction(&task)
	task.Step++
	if task.PlanID != "" {
		_, task, err = runtime.loadTaskPlan(ctx, task)
		if err != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "plan.unavailable", err)
			return paused, false, pauseErr
		}
	}
	if task.Status == TaskCancelled {
		task, err = runtime.cancelOwnedPlan(ctx, task, "The owning task was cancelled.")
		if err != nil {
			return task, false, err
		}
	}
	code := string(view.Status)
	if view.Outcome != nil {
		code = string(view.Outcome.Status)
	}
	appendTaskEvent(&task, operationTimelineEvent(
		task, "operation.terminal", view, runtime.now().UnixMilli(),
	))
	if cancelling && task.Status == TaskCancelled {
		appendTaskEvent(&task, TaskEvent{
			Kind: "task.cancelled", Step: task.Step, Code: code,
			OperationID: view.OperationID, AtUnixMillis: runtime.now().UnixMilli(),
		})
	}
	if warning {
		appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
	}
	saved, saveErr := runtime.saveTask(ctx, task)
	if saveErr == nil && task.Status == TaskCancelled {
		runtime.releaseController(saved)
	}
	return saved, saveErr == nil, saveErr
}

func operationResult(
	task TaskSession,
	view controlplane.OperationView,
) (*TaskOperationResult, error) {
	if task.PendingAction == nil {
		return nil, errors.New("terminal operation has no pending action")
	}
	summary := view.RejectionMessage
	if view.Outcome != nil {
		summary = view.Outcome.Summary
	}
	if summary == "" {
		summary = "The Host returned a terminal operation result."
	}
	var output json.RawMessage
	if len(view.Output) != 0 {
		encoded, err := json.Marshal(view.Output)
		if err != nil {
			return nil, err
		}
		output = encoded
	}
	result, err := sealTaskOperationResult(TaskOperationResult{
		OperationID: view.OperationID,
		Capability:  task.PendingAction.Capability,
		Status:      string(view.Status),
		Summary:     summary,
		Output:      output,
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func actionSubmissionRejectionCode(err error) string {
	switch {
	case errors.Is(err, controlplane.ErrStale):
		return "gateway.stale"
	case errors.Is(err, controlplane.ErrLeaseExpired):
		return "gateway.lease-expired"
	case errors.Is(err, controlplane.ErrForbidden), errors.Is(err, taskstate.ErrForbidden):
		return "gateway.forbidden"
	case errors.Is(err, controlplane.ErrInvalid):
		return "gateway.invalid"
	default:
		return "gateway.rejected"
	}
}

func (runtime *AgentRuntime) activatePendingMacro(
	ctx context.Context,
	task TaskSession,
	view controlplane.OperationView,
) (TaskSession, bool, error) {
	task.MacroOperationID = view.OperationID
	clearPendingTaskAction(&task)
	if task.Status != TaskCancelling {
		task.Status = TaskActive
	}
	task.PauseCode = ""
	task.Step++
	appendTaskEvent(&task, operationTimelineEvent(
		task, "macro.started", view, runtime.now().UnixMilli(),
	))
	saved, err := runtime.saveTask(ctx, task)
	return saved, err == nil, err
}

func macroOperationStarted(view controlplane.OperationView) bool {
	return !view.Terminal &&
		(view.Status == controlplane.OperationAccepted ||
			view.Status == controlplane.OperationRunning)
}

func (runtime *AgentRuntime) advanceMacroOperation(
	ctx context.Context,
	task TaskSession,
) (TaskSession, bool, error) {
	cancelling := task.Status == TaskCancelling
	var view controlplane.OperationView
	var err error
	if cancelling {
		view, err = runtime.control.CancelOperation(runtime.principal, task.MacroOperationID)
	} else {
		view, err = runtime.control.GetOperation(runtime.principal, task.MacroOperationID)
	}
	if err != nil {
		if cancelling {
			return task, false, err
		}
		paused, pauseErr := runtime.pauseTask(ctx, task, "operation.unavailable", err)
		return paused, false, pauseErr
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "macro.unknown", view)
	}
	if !view.Terminal && !cancelling &&
		view.Status != controlplane.OperationAwaitingConfirmation &&
		view.Status != controlplane.OperationAccepted &&
		view.Status != controlplane.OperationRunning {
		update, waitErr := runtime.control.WaitOperation(
			ctx,
			runtime.principal,
			controlplane.WaitOperationInput{
				OperationID: view.OperationID,
				AfterCursor: view.Cursor,
				WaitMillis:  runtime.operationWaitMillis,
			},
		)
		if waitErr != nil {
			return task, false, waitErr
		}
		view = update.Operation
		if !update.Changed && !view.Terminal {
			return task, false, nil
		}
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "macro.unknown", view)
	}
	if view.Status == controlplane.OperationAwaitingConfirmation {
		if task.Status != TaskWaitingConfirmation {
			task.Status = TaskWaitingConfirmation
			task.PauseCode = ""
			saved, saveErr := runtime.saveTask(ctx, task)
			return saved, false, saveErr
		}
		return task, false, nil
	}
	if !view.Terminal {
		if cancelling {
			return task, false, nil
		}
		if view.Status != controlplane.OperationAccepted &&
			view.Status != controlplane.OperationRunning {
			return task, false, nil
		}
		if task.Status == TaskWaitingConfirmation {
			task.Status = TaskActive
			task.PauseCode = ""
			var saveErr error
			task, saveErr = runtime.saveTask(ctx, task)
			if saveErr != nil {
				return task, false, saveErr
			}
		}
		return task, true, nil
	}
	if operationOutcomeIsUnknown(view) {
		return runtime.recordUnknownOperation(ctx, task, "macro.unknown", view)
	}
	warning := false
	if view.Outcome != nil && !runtime.outcomesRecordedByControl {
		warning = runtime.appendOutcomeMemory(ctx, task, view, "macro-outcome")
	}
	operationID := task.MacroOperationID
	task.MacroOperationID = ""
	if cancelling {
		task.Status = TaskCancelled
	} else {
		task.Status = TaskActive
	}
	if task.PlanID != "" {
		_, task, err = runtime.loadTaskPlan(ctx, task)
		if err != nil {
			return task, false, err
		}
	}
	if cancelling {
		task, err = runtime.cancelOwnedPlan(ctx, task, "The owning macro task was cancelled.")
		if err != nil {
			return task, false, err
		}
	}
	task.PauseCode = ""
	appendTaskEvent(&task, operationTimelineEvent(
		task, "macro.terminal", view, runtime.now().UnixMilli(),
	))
	if view.Outcome != nil && len(task.History) != 0 {
		task.History[len(task.History)-1].Code = string(view.Outcome.Status)
		task.History[len(task.History)-1].Summary = view.Outcome.Summary
	}
	if cancelling {
		appendTaskEvent(&task, TaskEvent{
			Kind: "task.cancelled", Step: task.Step, Code: string(view.Status),
			OperationID: operationID, AtUnixMillis: runtime.now().UnixMilli(),
		})
	}
	if warning {
		appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
	}
	saved, saveErr := runtime.saveTask(ctx, task)
	if saveErr == nil && cancelling {
		runtime.releaseController(saved)
	}
	return saved, saveErr == nil && !cancelling, saveErr
}

func operationOutcomeIsUnknown(view controlplane.OperationView) bool {
	if view.ReconciliationPending || view.Status == controlplane.OperationOutcomeUnknown {
		return true
	}
	if view.Status == controlplane.OperationSucceeded {
		return !view.ExecutionConfirmed || view.Outcome == nil || view.Outcome.Status != host.ActionSucceeded
	}
	if view.Outcome != nil {
		return false
	}
	switch view.Status {
	case controlplane.OperationRejected:
		return false
	case controlplane.OperationStale, controlplane.OperationCancelled:
		return view.DeliveryAttempts != 0
	case controlplane.OperationFailed, controlplane.OperationInterrupted:
		return true
	default:
		return view.DeliveryAttempts != 0
	}
}

func operationRequiresReconciliation(view controlplane.OperationView) bool {
	return view.ReconciliationPending || view.Status == controlplane.OperationOutcomeUnknown
}

func (runtime *AgentRuntime) recordUnknownOperation(
	ctx context.Context,
	task TaskSession,
	eventKind string,
	view controlplane.OperationView,
) (TaskSession, bool, error) {
	task.Status = TaskOutcomeUnknown
	task.PauseCode = "operation.outcome-unknown"
	appendTaskEvent(&task, operationTimelineEvent(
		task, eventKind, view, runtime.now().UnixMilli(),
	))
	saved, err := runtime.saveTask(ctx, task)
	if err == nil {
		runtime.releaseController(saved)
	}
	return saved, false, err
}

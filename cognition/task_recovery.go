package cognition

import (
	"context"

	"github.com/sunrioa/rin/controlplane"
)

// Unknown tasks stop deliberation, but must keep observing the original
// submission. Looking it up is the only permitted recovery path: never submit
// an uncertain intent again, even if its operation has expired from retention.
func (runtime *AgentRuntime) reconciledTaskOperation(task TaskSession) (controlplane.OperationView, bool) {
	id := task.PendingOperationID
	if id == "" {
		id = task.MacroOperationID
	}
	var view controlplane.OperationView
	var err error
	if task.PendingAction != nil && task.PendingOperationID == "" {
		view, err = runtime.control.FindActionOperation(runtime.principal, controlplane.SubmitActionInput{
			HostID: task.HostID, WorldID: task.WorldID, Request: *task.PendingAction,
			ParentOperationID: task.MacroOperationID,
		})
	} else if id != "" {
		view, err = runtime.control.GetOperation(runtime.principal, id)
	} else {
		return view, false
	}
	if err != nil || view.OperationID == "" || !view.Terminal || operationOutcomeIsUnknown(view) {
		return view, false
	}
	if pending, err := runtime.planProjectionPending(task, view); err != nil || pending {
		return view, false
	}
	return view, true
}

func (runtime *AgentRuntime) reconcileTaskOutcome(ctx context.Context, task TaskSession) (TaskSession, error) {
	view, ready := runtime.reconciledTaskOperation(task)
	if !ready {
		return task, nil
	}
	if task.PendingAction != nil && task.PendingOperationID == "" {
		task.PendingOperationID = view.OperationID
	}
	task.Status, task.PauseCode = TaskActive, ""
	if task.CancelRequested {
		task.Status = TaskCancelling
	}
	task.Schedule = TaskSchedule{Kind: ScheduleReady}
	appendTaskEvent(&task, operationTimelineEvent(task, "operation.reconciled", view, runtime.now().UnixMilli()))
	saved, err := runtime.saveTask(ctx, task)
	if err != nil {
		return task, err
	}
	if saved.CancelRequested {
		return runtime.reconcileCancellation(ctx, saved)
	}
	if saved.PendingAction != nil {
		advanced, _, err := runtime.advancePendingAction(ctx, saved)
		return advanced, err
	}
	advanced, _, err := runtime.advanceMacroOperation(ctx, saved)
	return advanced, err
}

func taskOccupiesActor(task TaskSession) bool {
	return !terminalTaskStatus(task.Status) || task.Status == TaskOutcomeUnknown
}

package cognition

import (
	"context"
	"errors"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

// TaskSchedule is durable control state, independent of the diagnostic history.
// A wait releases the task worker; changes or a recovery scan test readiness.
type TaskSchedule struct {
	Kind                     TaskScheduleKind `json:"kind"`
	OperationID              string           `json:"operation_id,omitempty"`
	AfterCursor              string           `json:"after_cursor,omitempty"`
	ObservationEpoch         *host.Epoch      `json:"observation_epoch,omitempty"`
	AfterObservationSequence uint64           `json:"after_observation_sequence,omitempty"`
	RetryAtUnixMillis        int64            `json:"retry_at_unix_millis,omitempty"`
}

type TaskScheduleKind string

const (
	ScheduleReady        TaskScheduleKind = "ready"
	ScheduleObservation  TaskScheduleKind = "waiting-observation"
	ScheduleOperation    TaskScheduleKind = "waiting-operation"
	ScheduleConfirmation TaskScheduleKind = "waiting-confirmation"
	ScheduleRetry        TaskScheduleKind = "retry-at"
	ScheduleUser         TaskScheduleKind = "waiting-user"
	ScheduleStopped      TaskScheduleKind = "stopped"
)

const automaticTaskRetryDelay = 5 * time.Second

// TaskReadyAt handles states that do not require a Host read. Observation and
// operation waits are evaluated by AgentRuntime.TaskReady.
func TaskReadyAt(task TaskSession, now time.Time) bool {
	if terminalTaskStatus(task.Status) {
		return false
	}
	switch task.Schedule.Kind {
	case ScheduleReady:
		return task.Status != TaskPaused
	case ScheduleRetry:
		return now.UnixMilli() >= task.Schedule.RetryAtUnixMillis
	default:
		return false
	}
}

// SchedulingEvents captures notification channels BEFORE a readiness scan so a
// publication between the scan and select cannot be lost. Custom control ports
// without notifications are supported by the scheduler's recovery timer.
func (runtime *AgentRuntime) SchedulingEvents() (<-chan struct{}, <-chan struct{}) {
	var controlChanges <-chan struct{}
	if source, ok := runtime.control.(interface{ Changes() <-chan struct{} }); ok {
		controlChanges = source.Changes()
	}
	return runtime.taskChangedChannel(), controlChanges
}

func (runtime *AgentRuntime) TaskReady(ctx context.Context, task TaskSession) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if task.Status == TaskOutcomeUnknown {
		_, ready := runtime.reconciledTaskOperation(task)
		return ready, nil
	}
	if terminalTaskStatus(task.Status) {
		return false, nil
	}
	if TaskReadyAt(task, runtime.now()) {
		return true, nil
	}
	switch task.Schedule.Kind {
	case ScheduleObservation:
		// A macro can settle without another observation being published.
		if task.MacroOperationID != "" {
			view, err := runtime.control.GetOperation(runtime.principal, task.MacroOperationID)
			if err != nil {
				return true, nil
			} // let the runtime persist the failure policy
			if view.Terminal || operationRequiresReconciliation(view) {
				return true, nil
			}
		}
		actor, err := runtime.control.GetActor(runtime.principal, task.HostID, task.WorldID, task.ActorID)
		if err != nil {
			return false, nil
		}
		return actor.Online && task.Schedule.ObservationEpoch != nil &&
			(actor.Epoch != *task.Schedule.ObservationEpoch || actor.ObservationSeq > task.Schedule.AfterObservationSequence), nil
	case ScheduleOperation, ScheduleConfirmation:
		view, err := runtime.control.GetOperation(runtime.principal, task.Schedule.OperationID)
		if err != nil {
			return true, nil
		}
		pending, err := runtime.planProjectionPending(task, view)
		if err != nil {
			return false, err
		}
		if pending {
			return false, nil
		}
		return view.Terminal || operationRequiresReconciliation(view) || view.Cursor != task.Schedule.AfterCursor, nil
	default:
		return false, nil
	}
}

func waitForObservation(task *TaskSession, observation host.ObservationEnvelope) {
	epoch := observation.Epoch
	task.Schedule = TaskSchedule{Kind: ScheduleObservation, ObservationEpoch: &epoch,
		AfterObservationSequence: observation.Sequence}
}

func (runtime *AgentRuntime) waitForOperation(ctx context.Context, task TaskSession, view controlplane.OperationView) (TaskSession, bool, error) {
	kind := ScheduleOperation
	if view.Status == controlplane.OperationAwaitingConfirmation {
		kind = ScheduleConfirmation
	}
	schedule := TaskSchedule{Kind: kind, OperationID: view.OperationID, AfterCursor: view.Cursor}
	if task.Schedule == schedule {
		return task, false, nil
	}
	task.Schedule = schedule
	saved, err := runtime.saveTask(ctx, task)
	return saved, false, err
}

func scheduleForStatus(task TaskSession) TaskSchedule {
	switch {
	case terminalTaskStatus(task.Status):
		return TaskSchedule{Kind: ScheduleStopped}
	case task.Status == TaskPaused:
		if task.Schedule.Kind == ScheduleRetry || task.Schedule.Kind == ScheduleUser {
			return task.Schedule
		}
		if automaticallyResumableTaskPause(task.PauseCode) {
			return TaskSchedule{Kind: ScheduleRetry, RetryAtUnixMillis: task.UpdatedAtUnixMillis + automaticTaskRetryDelay.Milliseconds()}
		}
		return TaskSchedule{Kind: ScheduleUser}
	case task.Schedule.Kind == "":
		return TaskSchedule{Kind: ScheduleReady}
	default:
		return task.Schedule
	}
}

func automaticallyResumableTaskPause(code string) bool {
	switch code {
	case "host.unavailable", "observation.unavailable", "controller.unavailable",
		"operation.unavailable", "action.submit-unavailable", "capabilities.unavailable", "plan.epoch-invalidated":
		return true
	default:
		return false
	}
}

// Only v3 import reads history. New tasks and the live scheduler never derive
// control flow from diagnostic events. A legacy wait resumes on a newer Host
// observation when one was recorded, otherwise it needs an explicit run.
func migrateTaskSchedule(task TaskSession) TaskSchedule {
	task.Schedule = TaskSchedule{}
	if task.Status == TaskActive && task.PendingAction == nil && task.MacroOperationID == "" {
		for i := len(task.History) - 1; i >= 0; i-- {
			switch task.History[i].Kind {
			case "provider.warning", "model.decision":
				continue
			case "task.wait":
				if task.LastObservationSeq > 0 {
					epoch := task.ControllerLease.Epoch
					return TaskSchedule{Kind: ScheduleObservation, ObservationEpoch: &epoch, AfterObservationSequence: task.LastObservationSeq}
				}
				return TaskSchedule{Kind: ScheduleUser}
			}
			break
		}
	}
	return scheduleForStatus(task)
}

func validateTaskSchedule(task TaskSession) error {
	schedule := task.Schedule
	switch schedule.Kind {
	case ScheduleReady, ScheduleUser, ScheduleStopped:
		if schedule.OperationID != "" || schedule.AfterCursor != "" || schedule.ObservationEpoch != nil || schedule.AfterObservationSequence != 0 || schedule.RetryAtUnixMillis != 0 {
			return errors.New("plain task schedule carries wait fields")
		}
	case ScheduleRetry:
		if task.Status != TaskPaused || schedule.RetryAtUnixMillis < 0 || schedule.RetryAtUnixMillis > maxProviderWireInteger || schedule.OperationID != "" || schedule.AfterCursor != "" || schedule.ObservationEpoch != nil || schedule.AfterObservationSequence != 0 {
			return errors.New("invalid retry schedule")
		}
	case ScheduleObservation:
		if task.Status != TaskActive || task.PendingAction != nil || schedule.ObservationEpoch == nil || schedule.AfterObservationSequence == 0 || schedule.AfterObservationSequence > maxProviderWireInteger || schedule.OperationID != "" || schedule.AfterCursor != "" || schedule.RetryAtUnixMillis != 0 {
			return errors.New("invalid observation wait")
		}
		if err := schedule.ObservationEpoch.Validate("schedule.observation_epoch"); err != nil {
			return err
		}
	case ScheduleOperation, ScheduleConfirmation:
		if schedule.OperationID == "" || (schedule.OperationID != task.PendingOperationID && schedule.OperationID != task.MacroOperationID) || schedule.ObservationEpoch != nil || schedule.AfterObservationSequence != 0 || schedule.RetryAtUnixMillis != 0 {
			return errors.New("invalid operation wait")
		}
		if err := validateProviderText("schedule.after_cursor", schedule.AfterCursor, 256, false); err != nil {
			return err
		}
	default:
		return errors.New("invalid task schedule kind")
	}
	if terminalTaskStatus(task.Status) != (schedule.Kind == ScheduleStopped) {
		return errors.New("terminal task schedule mismatch")
	}
	return nil
}

func (runtime *AgentRuntime) planProjectionPending(task TaskSession, view controlplane.OperationView) (bool, error) {
	if task.PlanID == "" || view.Outcome == nil {
		return false, nil
	}
	if source, ok := runtime.control.(interface {
		OutcomeProjectionPending(host.Principal, string, string) (bool, error)
	}); ok {
		return source.OutcomeProjectionPending(runtime.principal, view.OperationID, "task-plan")
	}
	return false, nil
}

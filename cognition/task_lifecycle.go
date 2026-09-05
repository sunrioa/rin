package cognition

import (
	"context"
	"errors"
	"fmt"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

func (runtime *AgentRuntime) StartTask(
	ctx context.Context,
	input StartTaskInput,
) (TaskSession, error) {
	return runtime.startTask(ctx, input, nil)
}

// StartSignalTask starts a task with trusted, process-local Signal evidence.
// The public Agent HTTP contract only exposes StartTask.
func (runtime *AgentRuntime) StartSignalTask(
	ctx context.Context,
	input StartTaskInput,
	signal timeline.SignalContextRef,
) (TaskSession, error) {
	if err := timeline.ValidateSignalContextRef(signal); err != nil {
		return TaskSession{}, err
	}
	return runtime.startTask(ctx, input, &signal)
}

func (runtime *AgentRuntime) startTask(
	ctx context.Context,
	input StartTaskInput,
	signalRef *timeline.SignalContextRef,
) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	sealed, err := sealStartTaskInput(input)
	if err != nil {
		return TaskSession{}, err
	}
	if sealed.PlanningMode != taskstate.PlanningDisabled && runtime.plans == nil {
		return TaskSession{}, errors.New("planning mode requires the shared task coordinator")
	}
	if _, err := runtime.persona.Load(ctx, PersonaRequest{
		ActorID: sealed.ActorID, ControllerID: sealed.ControllerID,
	}); err != nil {
		return TaskSession{}, fmt.Errorf("load task persona: %w", err)
	}
	actor, err := runtime.control.GetActor(
		runtime.principal, sealed.HostID, sealed.WorldID, sealed.ActorID,
	)
	if err != nil {
		return TaskSession{}, err
	}
	if !actor.Online {
		return TaskSession{}, controlplane.ErrUnavailable
	}
	target := controlplane.ActorControlTarget{
		HostID: sealed.HostID, WorldID: sealed.WorldID, ActorID: sealed.ActorID,
	}
	lease, err := runtime.control.AcquireController(runtime.principal, controlplane.AcquireControllerInput{
		ActorControlTarget: target, ControllerID: sealed.ControllerID,
		LeaseTTLMillis: runtime.controllerLeaseMillis,
	})
	if err != nil {
		return TaskSession{}, err
	}
	if lease.Source != controlplane.DecisionInternal {
		_ = runtime.control.ReleaseController(runtime.principal, target, lease.LeaseID)
		return TaskSession{}, errors.New("internal Agent Runtime acquired a non-internal controller lease")
	}
	now := runtime.now().UnixMilli()
	task := TaskSession{
		TaskID: sealed.TaskID, SessionID: actor.Epoch.SessionID,
		HostID: sealed.HostID, AdapterID: actor.AdapterID,
		WorldID: sealed.WorldID, ActorID: sealed.ActorID,
		ControllerID: sealed.ControllerID, Goal: sealed.Goal, Tags: sealed.Tags,
		AllowedCapabilities: sealed.AllowedCapabilities,
		PlanningMode:        sealed.PlanningMode,
		Schedule:            TaskSchedule{Kind: ScheduleReady},
		Status:              TaskActive, Budget: sealed.Budget, ControllerLease: lease,
		CreatedAtUnixMillis: now, UpdatedAtUnixMillis: now,
	}
	appendTaskEvent(&task, TaskEvent{
		Kind: "task.created", Step: 0, Summary: "Task accepted by the internal Agent Runtime.",
		AtUnixMillis: now,
	})
	if signalRef != nil {
		signal := *signalRef
		epoch := actor.Epoch
		appendTaskEvent(&task, TaskEvent{
			Kind: "signal.received", Step: 0,
			Summary:      "Internal initiative task created from a Host signal.",
			AtUnixMillis: now, ObservationSequence: actor.ObservationSeq,
			Epoch: &epoch, Signal: &signal,
		})
	}
	created, err := runtime.tasks.Create(ctx, task)
	if err != nil {
		_ = runtime.control.ReleaseController(runtime.principal, target, lease.LeaseID)
		return TaskSession{}, err
	}
	runtime.notifyTaskChanged()
	return created, nil
}

func (runtime *AgentRuntime) GetTask(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	return runtime.tasks.Load(ctx, taskID)
}

// SnapshotTasks returns the durable task projection used by application-level
// schedulers to recover unfinished work after a daemon restart.
func (runtime *AgentRuntime) SnapshotTasks(ctx context.Context) (TaskSnapshot, error) {
	return runtime.tasks.Snapshot(ctx)
}

func (runtime *AgentRuntime) ResumeTask(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	lock := runtime.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	task, err := runtime.tasks.Load(ctx, taskID)
	if err != nil {
		return TaskSession{}, err
	}
	if task.Status != TaskPaused {
		return task, nil
	}
	task.Status = TaskActive
	task.Schedule = TaskSchedule{Kind: ScheduleReady}
	task.PauseCode = ""
	task.UpdatedAtUnixMillis = runtime.now().UnixMilli()
	appendTaskEvent(&task, TaskEvent{
		Kind: "task.resumed", Step: task.Step, AtUnixMillis: task.UpdatedAtUnixMillis,
	})
	return runtime.saveTask(ctx, task)
}

// CancelTask persistently stops future deliberation. If an action has reached
// the Host, the task remains cancelling until the authoritative Operation
// settles; requesting cancellation is not itself proof that execution stopped.
func (runtime *AgentRuntime) CancelTask(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	if err := validateTaskID(taskID); err != nil {
		return TaskSession{}, err
	}
	var task TaskSession
	for {
		var err error
		task, err = runtime.tasks.Load(ctx, taskID)
		if err != nil || terminalTaskStatus(task.Status) {
			return task, err
		}
		if task.CancelRequested {
			break
		}
		task.CancelRequested = true
		task.Status = TaskCancelling
		task.PauseCode = ""
		task.Schedule = TaskSchedule{Kind: ScheduleReady}
		appendTaskEvent(&task, TaskEvent{Kind: "task.cancel-requested", Step: task.Step,
			OperationID: task.PendingOperationID, AtUnixMillis: runtime.now().UnixMilli()})
		task, err = runtime.saveTask(ctx, task)
		if errors.Is(err, ErrTaskRevisionConflict) {
			continue
		}
		if err != nil {
			return task, err
		}
		break
	}
	// Cancellation intent is durable before touching an in-flight call. This
	// path never waits for a model provider to honor cancellation.
	runtime.runsMu.Lock()
	if cancel := runtime.activeRuns[taskID]; cancel != nil {
		cancel()
	}
	runtime.runsMu.Unlock()
	lock := runtime.taskLock(taskID)
	if !lock.TryLock() {
		return task, nil
	}
	defer lock.Unlock()
	task, err := runtime.tasks.Load(ctx, taskID)
	if err != nil {
		return task, err
	}
	return runtime.reconcileCancellation(ctx, task)
}

func (runtime *AgentRuntime) reconcileCancellation(ctx context.Context, task TaskSession) (TaskSession, error) {
	task.Schedule = TaskSchedule{Kind: ScheduleReady}
	var err error
	if task.PendingAction != nil && task.PendingOperationID == "" && !task.ActionSubmissionStarted {
		clearPendingTaskAction(&task)
	}
	if task.PendingAction != nil && task.PendingOperationID == "" {
		// A crash or cancellation may have happened after the gateway committed
		// but before the task saved its Operation ID. Lookup never submits work.
		view, lookupErr := runtime.control.FindActionOperation(runtime.principal, controlplane.SubmitActionInput{
			HostID: task.HostID, WorldID: task.WorldID, Request: *task.PendingAction,
			ParentOperationID: task.MacroOperationID,
		})
		if errors.Is(lookupErr, controlplane.ErrNotFound) {
			// The gateway may have pruned an older operation. Absence after a
			// submitted intent is not proof that no effect occurred.
			task.Status = TaskOutcomeUnknown
			task.PauseCode = "action.submission-unknown"
			appendTaskEvent(&task, TaskEvent{Kind: "action.submission-unknown", Step: task.Step,
				Code: task.PauseCode, AtUnixMillis: runtime.now().UnixMilli()})
			saved, err := runtime.saveTask(ctx, task)
			if err == nil {
				runtime.releaseController(saved)
			}
			return saved, err
		} else if lookupErr != nil {
			return task, lookupErr
		} else {
			task.PendingOperationID = view.OperationID
			task, err = runtime.saveTask(ctx, task)
			if err != nil {
				return task, err
			}
		}
	}

	if task.PendingOperationID == "" && task.MacroOperationID == "" {
		return runtime.finishCancelledTask(ctx, task, "before-operation")
	}
	if task.PendingOperationID == "" && task.MacroOperationID != "" {
		clearPendingTaskAction(&task)
	}
	if task.Status != TaskCancelling {
		task.Status = TaskCancelling
		task.PauseCode = ""
		appendTaskEvent(&task, TaskEvent{
			Kind: "task.cancel-requested", Step: task.Step,
			OperationID: task.PendingOperationID, AtUnixMillis: runtime.now().UnixMilli(),
		})
		task, err = runtime.saveTask(ctx, task)
		if err != nil {
			return task, err
		}
	}
	if task.PendingOperationID != "" {
		var keepRunning bool
		task, keepRunning, err = runtime.advancePendingAction(ctx, task)
		if err != nil || !keepRunning || task.MacroOperationID == "" {
			return task, err
		}
	}
	settled, _, err := runtime.advanceMacroOperation(ctx, task)
	return settled, err
}

func sealStartTaskInput(input StartTaskInput) (StartTaskInput, error) {
	if err := validateTaskID(input.TaskID); err != nil {
		return StartTaskInput{}, err
	}
	for field, value := range map[string]string{
		"host_id": input.HostID, "world_id": input.WorldID,
		"actor_id": input.ActorID, "controller_id": input.ControllerID,
	} {
		if err := validateProviderID(field, value); err != nil {
			return StartTaskInput{}, err
		}
	}
	if err := validateProviderText("goal", input.Goal, 2_000, true); err != nil {
		return StartTaskInput{}, err
	}
	if input.PlanningMode == "" {
		input.PlanningMode = taskstate.PlanningDisabled
	}
	switch input.PlanningMode {
	case taskstate.PlanningDisabled, taskstate.PlanningAuto, taskstate.PlanningRequired:
	default:
		return StartTaskInput{}, errors.New("planning_mode is invalid")
	}
	var err error
	if input.Tags, err = normalizeProviderIDs("tags", input.Tags, 32); err != nil {
		return StartTaskInput{}, err
	}
	if input.AllowedCapabilities, err = normalizeProviderIDs(
		"allowed_capabilities",
		input.AllowedCapabilities,
		128,
	); err != nil {
		return StartTaskInput{}, err
	}
	if input.Budget, err = normalizeTaskBudget(input.Budget); err != nil {
		return StartTaskInput{}, err
	}
	return input, nil
}

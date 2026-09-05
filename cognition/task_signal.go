package cognition

import (
	"context"
	"errors"
	"slices"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

// TaskSignal is bounded, untrusted context. It never supplies action authority
// or replaces the fresh observation collected before every decision.
type TaskSignal struct {
	timeline.SignalContextRef
	Summary             string     `json:"summary"`
	Epoch               host.Epoch `json:"epoch"`
	ObservationSequence uint64     `json:"observation_sequence"`
	ExpiresAtUnixMillis int64      `json:"expires_at_unix_millis"`
}

type ActorSignalInput struct {
	Task           StartTaskInput
	Signal         TaskSignal
	Preempt        bool
	CooldownMillis uint32
}

type SignalHandlingResult struct {
	Status string
	Reason string
	TaskID string
}

// HandleActorSignal is process-local. Priority comes from configured initiative
// rules, never Host text. Urgent initiative may cancel ordinary initiative;
// caller-created tasks and other urgent tasks only receive context.
func (runtime *AgentRuntime) HandleActorSignal(ctx context.Context, input ActorSignalInput) (SignalHandlingResult, error) {
	if err := validateTaskSignal(input.Signal); err != nil {
		return SignalHandlingResult{}, err
	}
	if err := ValidateStartTaskInput(input.Task); err != nil {
		return SignalHandlingResult{}, err
	}
	if input.Signal.Epoch.WorldID != input.Task.WorldID {
		return SignalHandlingResult{}, errors.New("signal world mismatch")
	}
	if input.Signal.ExpiresAtUnixMillis <= runtime.now().UnixMilli() {
		return SignalHandlingResult{Status: "dropped", Reason: "expired"}, nil
	}
	previous, lookupErr := runtime.tasks.Load(ctx, input.Task.TaskID)
	if lookupErr == nil {
		if previous.HostID != input.Task.HostID || previous.WorldID != input.Task.WorldID || previous.ActorID != input.Task.ActorID || !slices.Contains(previous.SeenSignalIDs, input.Signal.SignalID) {
			return SignalHandlingResult{}, ErrProviderConflict
		}
		return SignalHandlingResult{Status: "started", Reason: "already-created", TaskID: previous.TaskID}, nil
	} else if !errors.Is(lookupErr, ErrProviderNotFound) {
		return SignalHandlingResult{}, lookupErr
	}
	snapshot, err := runtime.actorTasks(ctx, input.Task)
	if err != nil {
		return SignalHandlingResult{}, err
	}
	for _, task := range snapshot.Tasks {
		if task.HostID != input.Task.HostID || task.WorldID != input.Task.WorldID || task.ActorID != input.Task.ActorID {
			continue
		}
		if task.Status == TaskOutcomeUnknown {
			return SignalHandlingResult{Status: "dropped", Reason: "outcome-reconciliation-required", TaskID: task.TaskID}, nil
		}
		if terminalTaskStatus(task.Status) {
			continue
		}
		if task.CancelRequested || task.Status == TaskCancelling {
			return SignalHandlingResult{Status: "retry", Reason: "task-stopping", TaskID: task.TaskID}, nil
		}
		if input.Preempt && task.InitiativePriority == 1 {
			if _, err := runtime.CancelTask(ctx, task.TaskID); err != nil {
				return SignalHandlingResult{}, err
			}
			return SignalHandlingResult{Status: "retry", Reason: "preemption-requested", TaskID: task.TaskID}, nil
		}
		return runtime.attachTaskSignal(ctx, task.TaskID, input.Signal)
	}
	if !input.Preempt && input.CooldownMillis > 0 {
		for _, task := range snapshot.Tasks {
			if task.HostID == input.Task.HostID && task.WorldID == input.Task.WorldID && task.ActorID == input.Task.ActorID && task.InitiativePriority > 0 && runtime.now().UnixMilli()-task.CreatedAtUnixMillis < int64(input.CooldownMillis) {
				return SignalHandlingResult{Status: "dropped", Reason: "initiative-cooldown", TaskID: task.TaskID}, nil
			}
		}
	}
	priority := uint32(1)
	if input.Preempt {
		priority = 2
	}
	created, err := runtime.startTaskWithSignal(ctx, input.Task, &input.Signal.SignalContextRef, &input.Signal, priority)
	if err != nil {
		return SignalHandlingResult{}, err
	}
	return SignalHandlingResult{Status: "started", TaskID: created.TaskID}, nil
}

func (runtime *AgentRuntime) attachTaskSignal(ctx context.Context, taskID string, signal TaskSignal) (SignalHandlingResult, error) {
	lock := runtime.taskLock(taskID)
	if !lock.TryLock() {
		return SignalHandlingResult{Status: "retry", Reason: "task-running", TaskID: taskID}, nil
	}
	defer lock.Unlock()
	task, err := runtime.tasks.Load(ctx, taskID)
	if err != nil {
		return SignalHandlingResult{}, err
	}
	if terminalTaskStatus(task.Status) || task.CancelRequested {
		return SignalHandlingResult{Status: "retry", Reason: "task-changed", TaskID: taskID}, nil
	}
	if slices.Contains(task.SeenSignalIDs, signal.SignalID) {
		return SignalHandlingResult{Status: "merged", Reason: "duplicate", TaskID: taskID}, nil
	}
	status := "attached"
	index := slices.IndexFunc(task.PendingSignals, func(s TaskSignal) bool { return s.Kind == signal.Kind })
	if index >= 0 {
		task.PendingSignals[index] = signal
		status = "merged"
	} else {
		if len(task.PendingSignals) >= 8 {
			return SignalHandlingResult{Status: "retry", Reason: "task-signal-capacity", TaskID: taskID}, nil
		}
		task.PendingSignals = append(task.PendingSignals, signal)
	}
	rememberTaskSignal(&task, signal.SignalID)
	if task.Status == TaskActive && task.PendingAction == nil && task.MacroOperationID == "" {
		task.Schedule = TaskSchedule{Kind: ScheduleReady}
	}
	epoch, ref := signal.Epoch, signal.SignalContextRef
	appendTaskEvent(&task, TaskEvent{Kind: "signal.received", Code: status, Summary: "Host signal added to task context.", Step: task.Step,
		AtUnixMillis: runtime.now().UnixMilli(), Epoch: &epoch, ObservationSequence: signal.ObservationSequence, Signal: &ref})
	if _, err := runtime.saveTask(ctx, task); err != nil {
		return SignalHandlingResult{}, err
	}
	return SignalHandlingResult{Status: status, TaskID: taskID}, nil
}

func rememberTaskSignal(task *TaskSession, id string) {
	task.SeenSignalIDs = append(task.SeenSignalIDs, id)
	if len(task.SeenSignalIDs) > 64 {
		task.SeenSignalIDs = append([]string(nil), task.SeenSignalIDs[len(task.SeenSignalIDs)-64:]...)
	}
}

func validateTaskSignal(signal TaskSignal) error {
	if err := timeline.ValidateSignalContextRef(signal.SignalContextRef); err != nil {
		return err
	}
	if err := validateProviderText("signal.summary", signal.Summary, 1000, true); err != nil {
		return err
	}
	if err := signal.Epoch.Validate("signal.epoch"); err != nil {
		return err
	}
	if signal.ObservationSequence == 0 || signal.ObservationSequence > maxProviderWireInteger || signal.ExpiresAtUnixMillis <= 0 || signal.ExpiresAtUnixMillis > maxProviderWireInteger {
		return errors.New("invalid signal sequence or expiry")
	}
	return nil
}

func validateTaskSignals(task TaskSession) error {
	if task.InitiativePriority > 2 || len(task.PendingSignals) > 8 || len(task.SeenSignalIDs) > 64 {
		return errors.New("task signals exceed their bounds")
	}
	if _, err := normalizeProviderIDs("seen_signal_ids", task.SeenSignalIDs, 64); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, signal := range task.PendingSignals {
		if err := validateTaskSignal(signal); err != nil {
			return err
		}
		if signal.Epoch.WorldID != task.WorldID || seen[signal.Kind] || !slices.Contains(task.SeenSignalIDs, signal.SignalID) {
			return errors.New("task signal scope or identity mismatch")
		}
		seen[signal.Kind] = true
	}
	return nil
}

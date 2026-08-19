package agentapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

const (
	defaultWorkerCount       = 4
	defaultQueueCapacity     = 1_024
	defaultReconcileInterval = time.Second
	automaticResumeDelay     = 5 * time.Second
)

// Service owns only asynchronous task coordination. AgentRuntime remains the
// single implementation of cognition and action lifecycle semantics.
type Service struct {
	runtime TaskRuntime

	ctx      context.Context
	cancel   context.CancelFunc
	queue    chan string
	interval time.Duration

	mu        sync.Mutex
	scheduled map[string]struct{}
	closed    bool
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func New(options Options) (*Service, error) {
	if options.Runtime == nil {
		return nil, errors.New("task runtime is required")
	}
	workers := options.WorkerCount
	if workers == 0 {
		workers = defaultWorkerCount
	}
	if workers > 64 {
		return nil, errors.New("worker count must not exceed 64")
	}
	capacity := options.QueueCapacity
	if capacity == 0 {
		capacity = defaultQueueCapacity
	}
	if capacity > 100_000 {
		return nil, errors.New("queue capacity must not exceed 100000")
	}
	interval := options.ReconcileInterval
	if interval == 0 {
		interval = defaultReconcileInterval
	}
	if interval < 50*time.Millisecond || interval > time.Minute {
		return nil, errors.New("reconcile interval must be between 50 and 60000 milliseconds")
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		runtime: options.Runtime, ctx: ctx, cancel: cancel,
		queue: make(chan string, capacity), interval: interval,
		scheduled: make(map[string]struct{}),
	}
	snapshot, err := service.runtime.SnapshotTasks(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("restore task scheduler: %w", err)
	}
	for _, task := range snapshot.Tasks {
		if taskNeedsAutomaticRun(task) {
			service.enqueue(task.TaskID)
		}
	}
	for worker := uint32(0); worker < workers; worker++ {
		service.wg.Add(1)
		go service.worker()
	}
	service.wg.Add(1)
	go service.reconcile()
	return service, nil
}

func (service *Service) StartTask(
	ctx context.Context,
	principal host.Principal,
	input cognition.StartTaskInput,
) (TaskDispatch, error) {
	return service.startTask(ctx, principal, input)
}

// StartSignalTask is the process-local entry point used after signalbox has
// authenticated a current Host snapshot. It is not exposed by the Agent API.
func (service *Service) StartSignalTask(
	ctx context.Context,
	principal host.Principal,
	input cognition.StartTaskInput,
	signal timeline.SignalContextRef,
) (TaskDispatch, error) {
	if err := service.authorize(ctx, principal, ScopeTaskExecute); err != nil {
		return TaskDispatch{}, err
	}
	if err := cognition.ValidateStartTaskInput(input); err != nil {
		return TaskDispatch{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	runtime, ok := service.runtime.(signalTaskRuntime)
	if !ok {
		return TaskDispatch{}, ErrUnavailable
	}
	task, err := runtime.StartSignalTask(ctx, input, signal)
	if err != nil {
		return TaskDispatch{}, normalizeServiceError(err)
	}
	return TaskDispatch{Task: task, Scheduled: service.enqueue(task.TaskID)}, nil
}

func (service *Service) startTask(
	ctx context.Context,
	principal host.Principal,
	input cognition.StartTaskInput,
) (TaskDispatch, error) {
	if err := service.authorize(ctx, principal, ScopeTaskExecute); err != nil {
		return TaskDispatch{}, err
	}
	if err := cognition.ValidateStartTaskInput(input); err != nil {
		return TaskDispatch{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	task, err := service.runtime.StartTask(ctx, input)
	if err != nil {
		return TaskDispatch{}, normalizeServiceError(err)
	}
	return TaskDispatch{Task: task, Scheduled: service.enqueue(task.TaskID)}, nil
}

func (service *Service) GetTask(
	ctx context.Context,
	principal host.Principal,
	taskID string,
) (cognition.TaskSession, error) {
	if err := service.authorize(ctx, principal, ScopeTaskRead); err != nil {
		return cognition.TaskSession{}, err
	}
	if err := cognition.ValidateTaskID(taskID); err != nil {
		return cognition.TaskSession{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	task, err := service.runtime.GetTask(ctx, taskID)
	return task, normalizeServiceError(err)
}

func (service *Service) GetTaskTimeline(
	ctx context.Context,
	principal host.Principal,
	query timeline.Query,
) (timeline.Page, error) {
	if err := service.authorize(ctx, principal, ScopeTaskRead); err != nil {
		return timeline.Page{}, err
	}
	page, err := service.runtime.GetTaskTimeline(ctx, query)
	if err != nil {
		return timeline.Page{}, normalizeServiceError(err)
	}
	return page, nil
}

func (service *Service) WaitTaskTimeline(
	ctx context.Context,
	principal host.Principal,
	input timeline.WaitInput,
) (timeline.Update, error) {
	if err := service.authorize(ctx, principal, ScopeTaskRead); err != nil {
		return timeline.Update{}, err
	}
	update, err := service.runtime.WaitTaskTimeline(ctx, input)
	if err != nil {
		return timeline.Update{}, normalizeServiceError(err)
	}
	return update, nil
}

func (service *Service) RunTask(
	ctx context.Context,
	principal host.Principal,
	taskID string,
) (TaskDispatch, error) {
	if err := service.authorize(ctx, principal, ScopeTaskExecute); err != nil {
		return TaskDispatch{}, err
	}
	if err := cognition.ValidateTaskID(taskID); err != nil {
		return TaskDispatch{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	task, err := service.runtime.GetTask(ctx, taskID)
	if err != nil {
		return TaskDispatch{}, normalizeServiceError(err)
	}
	scheduled := false
	if taskCanRun(task.Status) {
		scheduled = service.enqueue(task.TaskID)
	}
	return TaskDispatch{Task: task, Scheduled: scheduled}, nil
}

func (service *Service) ResumeTask(
	ctx context.Context,
	principal host.Principal,
	taskID string,
) (TaskDispatch, error) {
	if err := service.authorize(ctx, principal, ScopeTaskExecute); err != nil {
		return TaskDispatch{}, err
	}
	if err := cognition.ValidateTaskID(taskID); err != nil {
		return TaskDispatch{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	task, err := service.runtime.ResumeTask(ctx, taskID)
	if err != nil {
		return TaskDispatch{}, normalizeServiceError(err)
	}
	scheduled := false
	if taskCanRun(task.Status) {
		scheduled = service.enqueue(task.TaskID)
	}
	return TaskDispatch{Task: task, Scheduled: scheduled}, nil
}

func (service *Service) CancelTask(
	ctx context.Context,
	principal host.Principal,
	taskID string,
) (TaskDispatch, error) {
	if err := service.authorize(ctx, principal, ScopeTaskCancel); err != nil {
		return TaskDispatch{}, err
	}
	if err := cognition.ValidateTaskID(taskID); err != nil {
		return TaskDispatch{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	task, err := service.runtime.CancelTask(ctx, taskID)
	if err != nil && task.TaskID == "" {
		return TaskDispatch{}, normalizeServiceError(err)
	}
	if err != nil {
		stored, loadErr := service.runtime.GetTask(ctx, taskID)
		if loadErr == nil {
			task = stored
		}
	}
	scheduled := false
	if task.Status == cognition.TaskCancelling {
		scheduled = service.enqueue(task.TaskID)
	}
	if task.Status == cognition.TaskCancelling {
		err = nil
	}
	return TaskDispatch{Task: task, Scheduled: scheduled}, normalizeServiceError(err)
}

func (service *Service) Close() {
	service.closeOnce.Do(func() {
		service.mu.Lock()
		service.closed = true
		service.mu.Unlock()
		service.cancel()
		service.wg.Wait()
	})
}

func (service *Service) authorize(
	ctx context.Context,
	principal host.Principal,
	required string,
) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.Lock()
	closed := service.closed
	service.mu.Unlock()
	if closed {
		return ErrUnavailable
	}
	if hasScope(principal, controlplane.ScopeHostAdmin) || hasScope(principal, required) {
		return nil
	}
	return ErrForbidden
}

func (service *Service) enqueue(taskID string) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return false
	}
	if _, exists := service.scheduled[taskID]; exists {
		return false
	}
	select {
	case service.queue <- taskID:
		service.scheduled[taskID] = struct{}{}
		return true
	default:
		return false
	}
}

func (service *Service) worker() {
	defer service.wg.Done()
	for {
		select {
		case <-service.ctx.Done():
			return
		case taskID := <-service.queue:
			if service.ctx.Err() != nil {
				return
			}
			task, err := service.runtime.GetTask(service.ctx, taskID)
			if err == nil && task.Status == cognition.TaskPaused &&
				taskNeedsAutomaticRunAt(task, time.Now()) {
				task, err = service.runtime.ResumeTask(service.ctx, taskID)
			}
			if err == nil && taskCanRun(task.Status) {
				_, _ = service.runtime.RunTask(service.ctx, taskID)
			}
			service.mu.Lock()
			delete(service.scheduled, taskID)
			service.mu.Unlock()
		}
	}
}

func (service *Service) reconcile() {
	defer service.wg.Done()
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		select {
		case <-service.ctx.Done():
			return
		case <-ticker.C:
			snapshot, err := service.runtime.SnapshotTasks(service.ctx)
			if err != nil {
				continue
			}
			for _, task := range snapshot.Tasks {
				if taskNeedsAutomaticRun(task) {
					service.enqueue(task.TaskID)
				}
			}
		}
	}
}

func taskNeedsAutomaticRun(task cognition.TaskSession) bool {
	return taskNeedsAutomaticRunAt(task, time.Now())
}

func taskNeedsAutomaticRunAt(task cognition.TaskSession, now time.Time) bool {
	switch task.Status {
	case cognition.TaskCancelling, cognition.TaskWaitingConfirmation:
		return true
	case cognition.TaskActive:
		if task.PendingAction != nil || task.PendingOperationID != "" ||
			task.MacroOperationID != "" {
			return true
		}
		for index := len(task.History) - 1; index >= 0; index-- {
			switch task.History[index].Kind {
			case "provider.warning", "model.decision":
				continue
			case "task.wait":
				return false
			case "task.created", "task.resumed", "operation.terminal", "action.rejected":
				return true
			default:
				return false
			}
		}
	case cognition.TaskPaused:
		if !automaticallyResumablePause(task.PauseCode) {
			return false
		}
		return task.UpdatedAtUnixMillis == 0 ||
			now.UnixMilli()-task.UpdatedAtUnixMillis >= automaticResumeDelay.Milliseconds()
	}
	return false
}

func automaticallyResumablePause(code string) bool {
	switch code {
	case "host.unavailable", "observation.unavailable", "controller.unavailable",
		"operation.unavailable", "action.submit-unavailable", "capabilities.unavailable",
		"model.unavailable", "plan.epoch-invalidated":
		return true
	default:
		return false
	}
}

func taskCanRun(status cognition.TaskStatus) bool {
	switch status {
	case cognition.TaskActive, cognition.TaskWaitingConfirmation, cognition.TaskCancelling:
		return true
	default:
		return false
	}
}

func hasScope(principal host.Principal, wanted string) bool {
	for _, scope := range principal.GrantedScopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func normalizeServiceError(err error) error {
	if err == nil {
		return nil
	}
	for _, target := range []error{
		ErrInvalid, ErrNotFound, ErrForbidden, ErrConflict, ErrCapacity, ErrUnavailable,
	} {
		if errors.Is(err, target) {
			return err
		}
	}
	switch {
	case errors.Is(err, timeline.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case host.IsValidationError(err), errors.Is(err, controlplane.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case errors.Is(err, cognition.ErrProviderNotFound), errors.Is(err, controlplane.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case errors.Is(err, cognition.ErrProviderConflict),
		errors.Is(err, cognition.ErrTaskRevisionConflict),
		errors.Is(err, controlplane.ErrConflict),
		errors.Is(err, controlplane.ErrLeaseConflict),
		errors.Is(err, controlplane.ErrStale):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case errors.Is(err, cognition.ErrProviderCapacity), errors.Is(err, controlplane.ErrCapacity):
		return fmt.Errorf("%w: %v", ErrCapacity, err)
	case errors.Is(err, controlplane.ErrForbidden):
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	case errors.Is(err, controlplane.ErrUnavailable),
		errors.Is(err, controlplane.ErrPersistence),
		errors.Is(err, controlplane.ErrClosed),
		errors.Is(err, controlplane.ErrDataLocked),
		errors.Is(err, controlplane.ErrLeaseExpired):
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	default:
		return err
	}
}

var _ TaskRuntime = (*cognition.AgentRuntime)(nil)

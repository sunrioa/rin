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
)

// Service owns only asynchronous task coordination. AgentRuntime remains the
// single implementation of cognition and action lifecycle semantics.
type Service struct {
	runtime TaskRuntime

	ctx          context.Context
	cancel       context.CancelFunc
	queue        chan string
	interval     time.Duration
	dispatchDone chan struct{}

	mu        sync.Mutex
	scheduled map[string]bool
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
		dispatchDone: make(chan struct{}, 1),
		scheduled:    make(map[string]bool),
	}
	snapshot, err := service.runtime.SnapshotTasks(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("restore task scheduler: %w", err)
	}
	for _, task := range snapshot.Tasks {
		if service.taskReady(task) {
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

// HandleActorSignal coordinates trusted local initiative; no public transport
// accepts preemption rules or a caller-authored Signal context.
func (service *Service) HandleActorSignal(ctx context.Context, principal host.Principal, input cognition.ActorSignalInput) (cognition.SignalHandlingResult, error) {
	if err := service.authorize(ctx, principal, ScopeTaskExecute); err != nil {
		return cognition.SignalHandlingResult{}, err
	}
	if input.Preempt {
		if err := service.authorize(ctx, principal, ScopeTaskCancel); err != nil {
			return cognition.SignalHandlingResult{}, err
		}
	}
	runtime, ok := service.runtime.(interface {
		HandleActorSignal(context.Context, cognition.ActorSignalInput) (cognition.SignalHandlingResult, error)
	})
	if !ok {
		return cognition.SignalHandlingResult{}, ErrUnavailable
	}
	result, err := runtime.HandleActorSignal(ctx, input)
	if err != nil {
		return result, normalizeServiceError(err)
	}
	// Runtime state notifications handle attachment and cancellation. Newly
	// created tasks are also queued directly for low startup latency.
	if result.Status == "started" {
		service.enqueue(result.TaskID)
	}
	return result, nil
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
		scheduled = service.enqueue(task.TaskID, true)
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
		scheduled = service.enqueue(task.TaskID, true)
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
		scheduled = service.enqueue(task.TaskID, true)
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

func (service *Service) enqueue(taskID string, explicit ...bool) bool {
	force := len(explicit) != 0 && explicit[0]
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return false
	}
	if _, exists := service.scheduled[taskID]; exists {
		if force {
			service.scheduled[taskID] = true
		}
		return false
	}
	select {
	case service.queue <- taskID:
		service.scheduled[taskID] = force
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
			service.runTask(taskID)
		}
	}
}

func (service *Service) runTask(taskID string) {
	wake := false
	defer func() {
		// Keep a TaskRuntime panic inside this dispatch so the worker remains
		// available and the reconciler can retry the durable task state.
		_ = recover()
		service.mu.Lock()
		delete(service.scheduled, taskID)
		service.mu.Unlock()
		if wake {
			select {
			case service.dispatchDone <- struct{}{}:
			default:
			}
		}
	}()
	if service.ctx.Err() != nil {
		return
	}
	task, err := service.runtime.GetTask(service.ctx, taskID)
	service.mu.Lock()
	force := service.scheduled[taskID]
	service.mu.Unlock()
	// A readiness snapshot can become stale while this task is queued. Explicit
	// run requests may override a wait; automatic dispatches must recheck it.
	if err == nil && !force && !service.taskReady(task) {
		return
	}

	if err == nil && task.Status == cognition.TaskPaused &&
		service.taskReady(task) {
		task, err = service.runtime.ResumeTask(service.ctx, taskID)
	}
	if err == nil && taskCanRun(task.Status) {
		_, err = service.runtime.RunTask(service.ctx, taskID)
		wake = err == nil
	}
}

func (service *Service) reconcile() {
	defer service.wg.Done()
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		var taskChanges, controlChanges <-chan struct{}
		if runtime, ok := service.runtime.(schedulingRuntime); ok {
			taskChanges, controlChanges = runtime.SchedulingEvents()
		}
		snapshot, err := service.runtime.SnapshotTasks(service.ctx)
		if err == nil {
			for _, task := range snapshot.Tasks {
				if service.taskReady(task) {
					service.enqueue(task.TaskID)
				}
			}
		}
		select {
		case <-service.ctx.Done():
			return
		case <-ticker.C:
		case <-taskChanges:
		case <-controlChanges:
		case <-service.dispatchDone:
		}
	}
}

func (service *Service) taskReady(task cognition.TaskSession) bool {
	if runtime, ok := service.runtime.(schedulingRuntime); ok {
		ready, err := runtime.TaskReady(service.ctx, task)
		return err == nil && ready
	}
	return cognition.TaskReadyAt(task, time.Now())
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

func (service *Service) ConfirmTaskCompletion(ctx context.Context, principal host.Principal, input CompletionConfirmationInput) (TaskDispatch, error) {
	if err := service.authorize(ctx, principal, ScopeTaskExecute); err != nil {
		return TaskDispatch{}, err
	}
	if err := cognition.ValidateTaskID(input.TaskID); err != nil {
		return TaskDispatch{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if input.ExpectedRevision == 0 || input.ExpectedRevision > 9_007_199_254_740_991 {
		return TaskDispatch{}, fmt.Errorf("%w: expected_revision is required", ErrInvalid)
	}
	runtime, ok := service.runtime.(completionRuntime)
	if !ok {
		return TaskDispatch{}, ErrUnavailable
	}
	task, err := runtime.ConfirmTaskCompletion(ctx, input.TaskID, input.ExpectedRevision)
	return TaskDispatch{Task: task}, normalizeServiceError(err)
}

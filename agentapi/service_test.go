package agentapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

func TestServiceRunsTasksAsynchronouslyDeduplicatesAndSleepsOnWait(t *testing.T) {
	runtime := newFakeTaskRuntime()
	runtime.blockRuns = true
	service := newTestAgentService(t, runtime, 1)
	defer service.Close()
	principal := taskPrincipal(agentapi.ScopeTaskRead, agentapi.ScopeTaskExecute)

	dispatch, err := service.StartTask(context.Background(), principal, startTaskInput("task.async"))
	if err != nil || !dispatch.Scheduled {
		t.Fatalf("StartTask = %+v, %v", dispatch, err)
	}
	waitSignal(t, runtime.runStarted, "background task start")
	duplicate, err := service.RunTask(context.Background(), principal, "task.async")
	if err != nil || duplicate.Scheduled {
		t.Fatalf("running task was queued twice: %+v, %v", duplicate, err)
	}
	runtime.releaseRun <- struct{}{}
	waitFor(t, func() bool { return runtime.startedCount() == 0 }, "first task completion")
	time.Sleep(180 * time.Millisecond)
	if count := runtime.runCount("task.async"); count != 1 {
		t.Fatalf("wait decision was automatically rerun %d times", count)
	}

	awakened, err := service.RunTask(context.Background(), principal, "task.async")
	if err != nil || !awakened.Scheduled {
		t.Fatalf("explicit run did not wake task: %+v, %v", awakened, err)
	}
	waitSignal(t, runtime.runStarted, "explicit task wake")
	runtime.releaseRun <- struct{}{}
	waitFor(t, func() bool { return runtime.startedCount() == 0 }, "second task completion")
}

func TestServiceRecoversEligibleTasksAndHonorsWorkerLimit(t *testing.T) {
	runtime := newFakeTaskRuntime()
	runtime.blockRuns = true
	for _, taskID := range []string{"task.one", "task.two", "task.three"} {
		runtime.tasks[taskID] = activeTask(taskID, "task.created")
	}
	service := newTestAgentService(t, runtime, 2)
	defer service.Close()
	waitFor(t, func() bool { return runtime.startedCount() == 2 }, "two recovered workers")
	if maximum := runtime.maximumConcurrent(); maximum != 2 {
		t.Fatalf("maximum concurrent runs = %d, want 2", maximum)
	}
	runtime.releaseRun <- struct{}{}
	waitSignal(t, runtime.runStarted, "third recovered task")
	runtime.releaseRun <- struct{}{}
	runtime.releaseRun <- struct{}{}
	waitFor(t, func() bool {
		return runtime.runCount("task.one")+runtime.runCount("task.two")+runtime.runCount("task.three") == 3
	}, "all recovered tasks")
	waitFor(t, func() bool { return runtime.startedCount() == 0 }, "recovered task completion")
}

func TestServiceRechecksActiveMacroWaitingForHostObservation(t *testing.T) {
	runtime := newFakeTaskRuntime()
	task := activeTask("task.macro-wait", "macro.started")
	task.MacroOperationID = "operation.macro-wait"
	runtime.tasks[task.TaskID] = task
	service := newTestAgentService(t, runtime, 1)
	defer service.Close()

	waitFor(t, func() bool { return runtime.runCount(task.TaskID) == 1 },
		"active macro observation recheck")
}

func TestServiceCloseCancelsRunningTask(t *testing.T) {
	runtime := newFakeTaskRuntime()
	runtime.blockRuns = true
	service := newTestAgentService(t, runtime, 1)
	principal := taskPrincipal(agentapi.ScopeTaskExecute)
	if _, err := service.StartTask(context.Background(), principal, startTaskInput("task.close")); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, runtime.runStarted, "running task")
	service.Close()
	select {
	case <-runtime.runCancelled:
	case <-time.After(time.Second):
		t.Fatal("service close did not cancel the running runtime call")
	}
	if _, err := service.RunTask(context.Background(), principal, "task.close"); !errors.Is(err, agentapi.ErrUnavailable) {
		t.Fatalf("closed service error = %v", err)
	}
}

func TestServiceEnforcesTaskScopes(t *testing.T) {
	runtime := newFakeTaskRuntime()
	service := newTestAgentService(t, runtime, 1)
	defer service.Close()
	none := taskPrincipal(controlplane.ScopeActorRead)
	if _, err := service.StartTask(context.Background(), none, startTaskInput("task.denied")); !errors.Is(err, agentapi.ErrForbidden) {
		t.Fatalf("StartTask without task.execute error = %v", err)
	}
	runtime.putTask(activeTask("task.existing", "task.wait"))
	if _, err := service.GetTask(context.Background(), none, "task.existing"); !errors.Is(err, agentapi.ErrForbidden) {
		t.Fatalf("GetTask without task.read error = %v", err)
	}
	if _, err := service.GetTaskTimeline(context.Background(), none, timeline.Query{
		TaskID: "task.existing",
	}); !errors.Is(err, agentapi.ErrForbidden) {
		t.Fatalf("GetTaskTimeline without task.read error = %v", err)
	}
	if _, err := service.CancelTask(context.Background(), none, "task.existing"); !errors.Is(err, agentapi.ErrForbidden) {
		t.Fatalf("CancelTask without task.cancel error = %v", err)
	}
	admin := taskPrincipal(controlplane.ScopeHostAdmin)
	if _, err := service.GetTask(context.Background(), admin, "task.existing"); err != nil {
		t.Fatalf("host.admin could not read task: %v", err)
	}
	if _, err := service.GetTaskTimeline(context.Background(), admin, timeline.Query{
		TaskID: "task.existing",
	}); err != nil {
		t.Fatalf("host.admin could not read task timeline: %v", err)
	}
}

func newTestAgentService(t *testing.T, runtime *fakeTaskRuntime, workers uint32) *agentapi.Service {
	t.Helper()
	service, err := agentapi.New(agentapi.Options{
		Runtime: runtime, WorkerCount: workers, QueueCapacity: 16,
		ReconcileInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func taskPrincipal(scopes ...string) host.Principal {
	return host.Principal{ID: "principal.tasks", GrantedScopes: scopes}
}

func startTaskInput(taskID string) cognition.StartTaskInput {
	return cognition.StartTaskInput{
		TaskID: taskID, HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.test", ControllerID: "controller.internal", Goal: "Test the task service.",
		AllowedCapabilities: []string{"dialogue.speak"},
	}
}

func activeTask(taskID, eventKind string) cognition.TaskSession {
	return cognition.TaskSession{
		TaskID: taskID, Status: cognition.TaskActive,
		History: []cognition.TaskEvent{{Kind: eventKind}},
	}
}

type fakeTaskRuntime struct {
	mu sync.Mutex

	tasks        map[string]cognition.TaskSession
	runs         map[string]int
	activeRuns   int
	maxRuns      int
	blockRuns    bool
	runStarted   chan string
	releaseRun   chan struct{}
	runCancelled chan struct{}
}

func newFakeTaskRuntime() *fakeTaskRuntime {
	return &fakeTaskRuntime{
		tasks: make(map[string]cognition.TaskSession), runs: make(map[string]int),
		runStarted: make(chan string, 16), releaseRun: make(chan struct{}, 16),
		runCancelled: make(chan struct{}, 16),
	}
}

func (runtime *fakeTaskRuntime) StartTask(
	ctx context.Context,
	input cognition.StartTaskInput,
) (cognition.TaskSession, error) {
	if err := ctx.Err(); err != nil {
		return cognition.TaskSession{}, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if _, exists := runtime.tasks[input.TaskID]; exists {
		return cognition.TaskSession{}, cognition.ErrProviderConflict
	}
	task := activeTask(input.TaskID, "task.created")
	task.Goal = input.Goal
	task.AllowedCapabilities = append([]string(nil), input.AllowedCapabilities...)
	runtime.tasks[input.TaskID] = task
	return task, nil
}

func (runtime *fakeTaskRuntime) GetTask(
	ctx context.Context,
	taskID string,
) (cognition.TaskSession, error) {
	if err := ctx.Err(); err != nil {
		return cognition.TaskSession{}, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	task, exists := runtime.tasks[taskID]
	if !exists {
		return cognition.TaskSession{}, cognition.ErrProviderNotFound
	}
	return task, nil
}

func (runtime *fakeTaskRuntime) GetTaskTimeline(
	ctx context.Context,
	query timeline.Query,
) (timeline.Page, error) {
	task, err := runtime.GetTask(ctx, query.TaskID)
	if err != nil {
		return timeline.Page{}, err
	}
	records := make([]timeline.Record, len(task.History))
	for index, event := range task.History {
		sequence := event.Sequence
		if sequence == 0 {
			sequence = uint64(index + 1)
		}
		records[index] = timeline.Record{Sequence: sequence, Event: timeline.Event{
			TaskID: task.TaskID, EventKind: event.Kind,
			OccurredAtUnixMillis: event.AtUnixMillis,
		}}
	}
	return timeline.BuildPage(timeline.Snapshot{
		TaskID: task.TaskID, Status: string(task.Status),
		LatestSequence: uint64(len(records)), Records: records,
	}, query)
}

func (runtime *fakeTaskRuntime) WaitTaskTimeline(
	ctx context.Context,
	input timeline.WaitInput,
) (timeline.Update, error) {
	page, err := runtime.GetTaskTimeline(ctx, input.Query())
	if err != nil {
		return timeline.Update{}, err
	}
	return timeline.Update{Timeline: page, Changed: len(page.Events) != 0}, nil
}

func (runtime *fakeTaskRuntime) SnapshotTasks(ctx context.Context) (cognition.TaskSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return cognition.TaskSnapshot{}, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	snapshot := cognition.TaskSnapshot{Version: cognition.TaskSnapshotVersion, Revision: 1}
	for _, task := range runtime.tasks {
		snapshot.Tasks = append(snapshot.Tasks, task)
	}
	return snapshot, nil
}

func (runtime *fakeTaskRuntime) ResumeTask(
	ctx context.Context,
	taskID string,
) (cognition.TaskSession, error) {
	task, err := runtime.GetTask(ctx, taskID)
	if err != nil {
		return task, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	task.Status = cognition.TaskActive
	task.History = append(task.History, cognition.TaskEvent{Kind: "task.resumed"})
	runtime.tasks[taskID] = task
	return task, nil
}

func (runtime *fakeTaskRuntime) CancelTask(
	ctx context.Context,
	taskID string,
) (cognition.TaskSession, error) {
	task, err := runtime.GetTask(ctx, taskID)
	if err != nil {
		return task, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	task.Status = cognition.TaskCancelled
	runtime.tasks[taskID] = task
	return task, nil
}

func (runtime *fakeTaskRuntime) RunTask(
	ctx context.Context,
	taskID string,
) (cognition.TaskSession, error) {
	runtime.mu.Lock()
	task, exists := runtime.tasks[taskID]
	if !exists {
		runtime.mu.Unlock()
		return cognition.TaskSession{}, cognition.ErrProviderNotFound
	}
	runtime.runs[taskID]++
	runtime.activeRuns++
	if runtime.activeRuns > runtime.maxRuns {
		runtime.maxRuns = runtime.activeRuns
	}
	block := runtime.blockRuns
	runtime.mu.Unlock()
	runtime.runStarted <- taskID
	if block {
		select {
		case <-ctx.Done():
			runtime.mu.Lock()
			runtime.activeRuns--
			runtime.mu.Unlock()
			runtime.runCancelled <- struct{}{}
			return task, ctx.Err()
		case <-runtime.releaseRun:
		}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.activeRuns--
	task.History = append(task.History, cognition.TaskEvent{Kind: "task.wait"})
	runtime.tasks[taskID] = task
	return task, nil
}

func (runtime *fakeTaskRuntime) runCount(taskID string) int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.runs[taskID]
}

func (runtime *fakeTaskRuntime) putTask(task cognition.TaskSession) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.tasks[task.TaskID] = task
}

func (runtime *fakeTaskRuntime) startedCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.activeRuns
}

func (runtime *fakeTaskRuntime) maximumConcurrent() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.maxRuns
}

func waitSignal(t *testing.T, signal <-chan string, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitFor(t *testing.T, condition func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

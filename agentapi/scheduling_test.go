package agentapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
)

type eventTaskRuntime struct {
	*fakeTaskRuntime
	changed chan struct{}
	ready   map[string]bool
}

func (runtime *eventTaskRuntime) SchedulingEvents() (<-chan struct{}, <-chan struct{}) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.changed, nil
}
func (runtime *eventTaskRuntime) TaskReady(_ context.Context, task cognition.TaskSession) (bool, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if task.Schedule.Kind == cognition.ScheduleObservation {
		return runtime.ready[task.TaskID], nil
	}
	return cognition.TaskReadyAt(task, time.Now()), nil
}
func (runtime *eventTaskRuntime) publish(taskID string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.ready[taskID] = true
	close(runtime.changed)
	runtime.changed = make(chan struct{})
}

func TestSchedulerWakesOnEventAndDoesNotOccupyWorkerWhileWaiting(t *testing.T) {
	runtime := &eventTaskRuntime{fakeTaskRuntime: newFakeTaskRuntime(), changed: make(chan struct{}), ready: make(map[string]bool)}
	waiting := activeTask("task.waiting", "diagnostic.new-event")
	waiting.Schedule = cognition.TaskSchedule{Kind: cognition.ScheduleObservation}
	runtime.tasks[waiting.TaskID] = waiting
	runtime.tasks["task.ready"] = activeTask("task.ready", "signal.received")
	service, err := agentapi.New(agentapi.Options{Runtime: runtime, WorkerCount: 1, ReconcileInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	waitFor(t, func() bool { return runtime.runCount("task.ready") == 1 }, "ready task with one worker")
	if runtime.runCount(waiting.TaskID) != 0 {
		t.Fatal("waiting task occupied worker")
	}
	runtime.publish(waiting.TaskID)
	waitFor(t, func() bool { return runtime.runCount(waiting.TaskID) == 1 }, "event-driven wake before minute recovery scan")
}

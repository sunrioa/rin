package cognition_test

import (
	"context"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/timeline"
)

func TestTaskWaitPersistsObservationConditionAndIgnoresHistory(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{Kind: cognition.ModelDecisionWait, Summary: "Wait for a new observation."}, {Kind: cognition.ModelDecisionComplete, Summary: "Done."}}
	runtime := fixture.runtime(t, 16)
	started := fixture.start(t, runtime, "task.wait-explicit")
	task, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Schedule.Kind != cognition.ScheduleObservation || task.Schedule.AfterObservationSequence != fixture.environment.observation.Sequence {
		t.Fatalf("wait schedule = %#v", task.Schedule)
	}
	task.History = append(task.History, cognition.TaskEvent{Kind: "diagnostics.custom"})
	ready, err := runtime.TaskReady(context.Background(), task)
	if err != nil || ready {
		t.Fatalf("unchanged observation was ready: %v %v", ready, err)
	}
	snapshot, err := fixture.tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.tasks, err = cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restarted := fixture.runtime(t, 16)
	restored, err := restarted.GetTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.control.actor.ObservationSeq++
	fixture.environment.observation.Sequence++
	fixture.environment.observation.ObservationID = "observation.2"
	ready, err = restarted.TaskReady(context.Background(), restored)
	if err != nil || !ready {
		t.Fatalf("new observation did not wake restored wait: %v %v", ready, err)
	}
	completed, err := restarted.RunTask(context.Background(), task.TaskID)
	if err != nil || completed.Status != cognition.TaskCompleted {
		t.Fatalf("woken task = %#v, %v", completed, err)
	}
}

func TestSignalTaskIsRunnableWithoutReadingItsHistory(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	runtime := fixture.runtime(t, 1)
	task, err := runtime.StartSignalTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.signal-recovery", HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira", ControllerID: "controller.internal", Goal: "Respond to the event.",
	}, timeline.SignalContextRef{SignalID: "signal.one", Kind: "world.event", Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	if task.History[len(task.History)-1].Kind != "signal.received" || !cognition.TaskReadyAt(task, time.Now()) {
		t.Fatal("signal event suppressed initial scheduling")
	}
	snapshot, err := fixture.tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	task, err = restored.Load(context.Background(), task.TaskID)
	if err != nil || !cognition.TaskReadyAt(task, time.Now()) {
		t.Fatalf("signal task lost after restart: %v", err)
	}
}

func TestLegacyWaitMigratesOnceAndRetainsManualFallback(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{Kind: cognition.ModelDecisionWait, Summary: "Waiting."}}
	runtime := fixture.runtime(t, 16)
	started := fixture.start(t, runtime, "task.legacy-wait")
	if _, err := runtime.RunTask(context.Background(), started.TaskID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Version = "rin.cognition.tasks/v3"
	snapshot.Tasks[0].Schedule = cognition.TaskSchedule{}
	restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	task, err := restored.Load(context.Background(), started.TaskID)
	if err != nil || task.Schedule.Kind != cognition.ScheduleObservation {
		t.Fatalf("legacy observation wait = %#v %v", task.Schedule, err)
	}
	snapshot.Tasks[0].LastObservationID = ""
	snapshot.Tasks[0].LastObservationSeq = 0
	restored, err = cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	task, err = restored.Load(context.Background(), started.TaskID)
	if err != nil || task.Schedule.Kind != cognition.ScheduleUser {
		t.Fatalf("legacy wait without observation = %#v %v", task.Schedule, err)
	}
}

func TestPendingOperationYieldsWithoutLongPoll(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	fixture.control.operationAfterSubmit = queuedAgentOperation()
	runtime := fixture.runtime(t, 16)
	task := fixture.start(t, runtime, "task.nonblocking-operation")
	pending, err := runtime.RunTask(context.Background(), task.TaskID)
	if err != nil || pending.Schedule.Kind != cognition.ScheduleOperation {
		t.Fatalf("operation schedule = %#v %v", pending.Schedule, err)
	}
	ready, err := runtime.TaskReady(context.Background(), pending)
	if err != nil || ready {
		t.Fatalf("unchanged operation was runnable: %v %v", ready, err)
	}
	fixture.control.operationAfterSubmit.Status = controlplane.OperationRunning
	fixture.control.operationAfterSubmit.Cursor = "cursor.new-progress"
	ready, err = runtime.TaskReady(context.Background(), pending)
	if err != nil || !ready {
		t.Fatalf("operation progress did not wake: %v %v", ready, err)
	}
}

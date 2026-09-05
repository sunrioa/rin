package cognition_test

import (
	"context"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/timeline"
)

func actorSignal(fixture *agentRuntimeFixture, id string, priority bool) cognition.ActorSignalInput {
	return cognition.ActorSignalInput{Task: cognition.StartTaskInput{TaskID: "task." + id, HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira", ControllerID: "controller.internal", Goal: "Respond to the latest event."},
		Signal: cognition.TaskSignal{SignalContextRef: timeline.SignalContextRef{SignalID: id, Kind: "game.notice", Cursor: 1}, Summary: "A nearby player called.", Epoch: fixture.control.actor.Epoch,
			ObservationSequence: fixture.control.actor.ObservationSeq, ExpiresAtUnixMillis: fixture.now().Add(time.Minute).UnixMilli()}, Preempt: priority}
}

func TestActorSignalMergesWakesAndRestoresContextForLatestObservation(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{Kind: cognition.ModelDecisionWait, Summary: "Wait."}, {Kind: cognition.ModelDecisionWait, Summary: "Notice."}}
	runtime := fixture.runtime(t, 16)
	task := fixture.start(t, runtime, "task.current")
	if _, err := runtime.RunTask(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	first := actorSignal(fixture, "signal.one", false)
	result, err := runtime.HandleActorSignal(context.Background(), first)
	if err != nil || result.Status != "attached" || result.TaskID != task.TaskID {
		t.Fatalf("attach = %#v %v", result, err)
	}
	second := actorSignal(fixture, "signal.two", false)
	second.Signal.Summary = "The player called again."
	result, err = runtime.HandleActorSignal(context.Background(), second)
	if err != nil || result.Status != "merged" {
		t.Fatalf("merge = %#v %v", result, err)
	}
	snapshot, _ := fixture.tasks.Snapshot(context.Background())
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Schedule.Kind != cognition.ScheduleReady || len(snapshot.Tasks[0].PendingSignals) != 1 {
		t.Fatalf("signal spawned or failed to wake task: %#v", snapshot)
	}
	fixture.tasks, err = cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	runtime = fixture.runtime(t, 16)
	result, err = runtime.HandleActorSignal(context.Background(), first)
	if err != nil || result.Reason != "duplicate" {
		t.Fatalf("durable dedup = %#v %v", result, err)
	}
	advanceAgentObservation(fixture)
	if _, err := runtime.RunTask(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	input := fixture.model.inputs[len(fixture.model.inputs)-1]
	if len(input.Task.Signals) != 1 || input.Task.Signals[0].SignalID != "signal.two" || input.Observation.Sequence <= input.Task.Signals[0].ObservationSequence {
		t.Fatalf("not current observation with merged context: %#v", input)
	}
	stored, _ := runtime.GetTask(context.Background(), task.TaskID)
	if len(stored.PendingSignals) != 0 || len(stored.SeenSignalIDs) != 2 {
		t.Fatalf("context not consumed durably: %#v", stored)
	}
}

func TestUrgentSignalWaitsForHostStopBeforeStartingReplacement(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	running := queuedAgentOperation()
	running.Status = controlplane.OperationRunning
	running.DeliveryAttempts = 1
	fixture.control.operationAfterSubmit = running
	runtime := fixture.runtime(t, 16)
	ordinary := actorSignal(fixture, "signal.ordinary", false)
	started, err := runtime.HandleActorSignal(context.Background(), ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTask(context.Background(), started.TaskID); err != nil {
		t.Fatal(err)
	}
	urgent := actorSignal(fixture, "signal.urgent", true)
	result, err := runtime.HandleActorSignal(context.Background(), urgent)
	if err != nil || result.Reason != "preemption-requested" {
		t.Fatalf("preempt = %#v %v", result, err)
	}
	result, err = runtime.HandleActorSignal(context.Background(), urgent)
	if err != nil || result.Reason != "task-stopping" {
		t.Fatalf("replaced before Host stop: %#v %v", result, err)
	}
	fixture.control.cancelResult = cancelledAgentOperation(fixture.environment.observation)
	fixture.control.operationAfterSubmit = fixture.control.cancelResult
	if _, err := runtime.RunTask(context.Background(), started.TaskID); err != nil {
		t.Fatal(err)
	}
	result, err = runtime.HandleActorSignal(context.Background(), urgent)
	if err != nil || result.Status != "started" || result.TaskID != urgent.Task.TaskID {
		t.Fatalf("replacement = %#v %v", result, err)
	}
	duplicate, err := runtime.HandleActorSignal(context.Background(), urgent)
	if err != nil || duplicate.Reason != "already-created" {
		t.Fatalf("replayed creation = %#v %v", duplicate, err)
	}
}

func TestUrgentSignalPreservesCallerTaskAndManualReview(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{Kind: cognition.ModelDecisionComplete, Summary: "Review."}}
	runtime := fixture.runtime(t, 16)
	task := startCompletionTask(t, runtime, cognition.TaskCompletionPolicy{Mode: cognition.CompletionHuman})
	if _, err := runtime.RunTask(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.HandleActorSignal(context.Background(), actorSignal(fixture, "signal.urgent", true))
	if err != nil || result.Status != "attached" {
		t.Fatalf("caller task preempted: %#v %v", result, err)
	}
	stored, _ := runtime.GetTask(context.Background(), task.TaskID)
	if stored.CancelRequested || stored.Status != cognition.TaskPaused || stored.Schedule.Kind != cognition.ScheduleUser {
		t.Fatalf("signal bypassed manual review: %#v", stored)
	}
}

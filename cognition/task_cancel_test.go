package cognition_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

type cancellableModel struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (model *cancellableModel) Decide(ctx context.Context, _ cognition.ModelInput) (cognition.ModelDecision, error) {
	close(model.started)
	<-ctx.Done()
	close(model.cancelled)
	// Simulate a provider that returns a successful but late result after it was
	// told to stop. The runtime must discard it and must not submit its action.
	<-model.release
	return agentActionDecision(), nil
}
func (*cancellableModel) Health(context.Context) cognition.ProviderHealth {
	return cognition.ProviderHealth{Available: true}
}

func TestCancelPersistsWhileModelIsRunningAndRejectsLateDecision(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	tasks, err := cognition.OpenFileTaskStore(filepath.Join(t.TempDir(), "tasks.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tasks.Close()
	model := &cancellableModel{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: fixture.principal, Control: fixture.control, Environment: fixture.environment, Persona: fixture.persona,
		Model: model, Tasks: tasks, Now: fixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := fixture.start(t, runtime, "task.cancel-in-model")
	runDone := make(chan error, 1)
	go func() { _, err := runtime.RunTask(context.Background(), started.TaskID); runDone <- err }()
	awaitTaskSignal(t, model.started)
	cancelDone := make(chan cognition.TaskSession, 1)
	cancelErrors := make(chan error, 1)
	go func() {
		task, err := runtime.CancelTask(context.Background(), started.TaskID)
		cancelDone <- task
		cancelErrors <- err
	}()
	var cancelling cognition.TaskSession
	select {
	case cancelling = <-cancelDone:
	case <-time.After(time.Second):
		close(model.release)
		t.Fatal("cancel waited for model result")
	}
	if err := <-cancelErrors; err != nil {
		close(model.release)
		t.Fatal(err)
	}
	if !cancelling.CancelRequested || cancelling.Status != cognition.TaskCancelling {
		t.Fatalf("cancellation not persisted: %#v", cancelling)
	}
	awaitTaskSignal(t, model.cancelled)
	// The cancellation is in the durable snapshot while the old run still holds
	// its execution lock; it is sufficient to resume cleanup after a restart.
	snapshot, err := tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Tasks[0].CancelRequested {
		t.Fatal("snapshot lost cancel intent")
	}
	close(model.release)
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late model error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not exit")
	}
	if len(fixture.control.submissions) != 0 {
		t.Fatal("late model action was submitted")
	}
	restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.tasks = restored
	restarted := fixture.runtime(t, 16)
	cancelled, err := restarted.RunTask(context.Background(), started.TaskID)
	if err != nil || cancelled.Status != cognition.TaskCancelled {
		t.Fatalf("cancel recovery=%#v, %v", cancelled, err)
	}
	if len(fixture.control.submissions) != 0 {
		t.Fatal("cancel recovery submitted an action")
	}
}

type blockedSubmissionControl struct {
	*fakeAgentControlPlane
	started chan struct{}
	release chan struct{}
}

func (control *blockedSubmissionControl) SubmitAction(ctx context.Context, principal host.Principal, input controlplane.SubmitActionInput) (controlplane.OperationView, error) {
	view, err := control.fakeAgentControlPlane.SubmitAction(ctx, principal, input)
	if err != nil {
		return view, err
	}
	close(control.started)
	<-control.release
	return view, nil
}

func TestCancellationRecoversCommittedSubmissionWithoutResubmitting(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	fixture.control.operationAfterSubmit = queuedAgentOperation()
	fixture.control.cancelResult = cancelledAgentOperation(fixture.environment.observation)
	control := &blockedSubmissionControl{fakeAgentControlPlane: fixture.control, started: make(chan struct{}), release: make(chan struct{})}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: fixture.principal, Control: control, Environment: fixture.environment, Persona: fixture.persona, Model: fixture.model, Tasks: fixture.tasks, Now: fixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := fixture.start(t, runtime, "task.cancel-in-submit")
	done := make(chan error, 1)
	go func() { _, err := runtime.RunTask(context.Background(), task.TaskID); done <- err }()
	awaitTaskSignal(t, control.started)
	cancelled, err := runtime.CancelTask(context.Background(), task.TaskID)
	if err != nil || !cancelled.CancelRequested || cancelled.PendingOperationID != "" {
		t.Fatalf("inflight cancel=%#v %v", cancelled, err)
	}
	close(control.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("submission did not return")
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
	settled, err := restarted.RunTask(context.Background(), task.TaskID)
	if err != nil || settled.Status != cognition.TaskCancelled {
		t.Fatalf("recovered cancellation=%#v %v", settled, err)
	}
	if len(fixture.control.submissions) != 1 || fixture.control.cancelCalls != 1 {
		t.Fatalf("recovery resubmitted: submits=%d cancels=%d", len(fixture.control.submissions), fixture.control.cancelCalls)
	}
}

func awaitTaskSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task signal")
	}
}

func TestCancellationDoesNotClaimSuccessWhenSubmissionRecordIsMissing(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	runtime := fixture.runtime(t, 1)
	task := fixture.start(t, runtime, "task.cancel-missing")
	task, err := runtime.RunTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	task.ActionSubmissionStarted = true // simulate crash after dispatch intent was committed
	task, err = fixture.tasks.CompareAndSwap(context.Background(), task.Revision, task)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := runtime.CancelTask(context.Background(), task.TaskID)
	if err != nil || cancelled.Status != cognition.TaskOutcomeUnknown || cancelled.PendingAction == nil {
		t.Fatalf("missing evidence was treated as confirmed cancellation: %#v %v", cancelled, err)
	}
	if len(fixture.control.submissions) != 0 {
		t.Fatal("cancellation retried uncertain action")
	}
}

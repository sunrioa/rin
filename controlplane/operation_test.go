package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestActionOperationReportsHostLifecycleAndOutput(t *testing.T) {
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	input := actionHost.input("request.operation.lifecycle", "action.operation.lifecycle")
	operation, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	retried, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil || retried.OperationID != operation.OperationID {
		t.Fatalf("idempotent SubmitAction = %#v, %v", retried, err)
	}
	first := pollHost(t, service, lease, 8)
	second := pollHost(t, service, lease, 8)
	if len(first.Requests) != 1 || len(second.Requests) != 1 ||
		first.Requests[0].DeliveryAttempt != 1 ||
		second.Requests[0].DeliveryAttempt != 2 ||
		first.Requests[0].Request.Kind != ControlAction {
		t.Fatalf("Host redelivery = %#v, %#v", first, second)
	}
	ack := HostAcknowledgement{OperationID: operation.OperationID, Accepted: true}
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, ack); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.ReportHostRun("test.host", lease.LeaseID, host.ActionRun{
		OperationID: operation.OperationID,
		Status:      host.ActionRunning,
		ProgressSeq: 1,
		Progress:    50,
		UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 11},
	}); err != nil {
		t.Fatalf("ReportHostRun: %v", err)
	}
	outcome := host.ActionOutcome{
		OperationID: operation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     "The Host applied the action.",
		Epoch:       testEpoch(),
		WorldSeq:    2,
		OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 12},
	}
	if err := service.ReportHostResult(
		"test.host",
		lease.LeaseID,
		outcome,
		json.RawMessage(`{"type":"action_result","moved":true}`),
	); err != nil {
		t.Fatalf("ReportHostResult: %v", err)
	}
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationSucceeded || !view.Terminal ||
		!view.ExecutionConfirmed || view.Output["moved"] != true || view.Run == nil ||
		view.Run.Status != host.ActionRunning {
		t.Fatalf("GetOperation = %#v, %v", view, err)
	}
}

func TestActionOperationPublishesCommittedOutcomeEvidence(t *testing.T) {
	sink := &recordingOutcomeSink{evidence: make(chan OutcomeEvidence, 8)}
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{OutcomeSink: sink})
	input := actionHost.input("request.outcome.sink", "action.outcome.sink")
	input.Request.TaskID = "task.outcome.sink"
	operation, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host", lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatal(err)
	}
	outcome := host.ActionOutcome{
		OperationID: operation.OperationID, Status: host.ActionSucceeded,
		Summary: "The Host confirmed the effect.", Epoch: testEpoch(), WorldSeq: 2,
		OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
	}
	if err := service.ReportHostOutcome("test.host", lease.LeaseID, outcome); err != nil {
		t.Fatal(err)
	}
	select {
	case evidence := <-sink.evidence:
		if evidence.TaskID != input.Request.TaskID || evidence.OperationID != operation.OperationID || evidence.Outcome.Summary != outcome.Summary {
			t.Fatalf("outcome evidence = %#v", evidence)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("committed outcome was not delivered")
	}
	waitForOutcomeDelivery(t, service, principal, operation.OperationID, "default")
	if err := service.ReportHostOutcome("test.host", lease.LeaseID, outcome); err != nil {
		t.Fatal(err)
	}
	select {
	case evidence := <-sink.evidence:
		t.Fatalf("acknowledged outcome was redelivered: %#v", evidence)
	case <-time.After(30 * time.Millisecond):
	}
}

type recordingOutcomeSink struct{ evidence chan OutcomeEvidence }

func (sink *recordingOutcomeSink) RecordOutcome(_ context.Context, evidence OutcomeEvidence) error {
	sink.evidence <- evidence
	return nil
}

func waitForOutcomeDelivery(t *testing.T, service *Service, principal host.Principal, operationID, subscriber string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := service.OutcomeProjectionPending(principal, operationID, subscriber)
		if err != nil {
			t.Fatal(err)
		}
		if !pending {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("outcome delivery did not settle: %s/%s", operationID, subscriber)
}

func TestHostOutcomeCannotPredateBoundObservation(t *testing.T) {
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	publication := worldPublication(2, "newer")
	publication.Actors[0].ObservationSeq = 2
	if err := service.PublishWorld("test.host", lease.LeaseID, publication); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	actionHost.snapshot.ObservationSeq = 2
	input := actionHost.input("request.outcome.sequence", "action.outcome.sequence")
	input.Request.ObservationSeq = 2
	operation, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	err = service.ReportHostOutcome("test.host", lease.LeaseID, host.ActionOutcome{
		OperationID: operation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     "This result predates the binding.",
		Epoch:       testEpoch(),
		WorldSeq:    1,
		OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 12},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("predated outcome error = %v", err)
	}
}

func TestWaitOperationWakesForDeliveryWithoutClaimingExecution(t *testing.T) {
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	operation, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.wait.delivery", "action.wait.delivery"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	result := make(chan OperationUpdate, 1)
	failure := make(chan error, 1)
	go func() {
		update, waitErr := service.WaitOperation(context.Background(), principal,
			WaitOperationInput{
				OperationID: operation.OperationID,
				AfterCursor: operation.Cursor,
				WaitMillis:  1_000,
			})
		if waitErr != nil {
			failure <- waitErr
			return
		}
		result <- update
	}()
	time.Sleep(10 * time.Millisecond)
	pollHost(t, service, lease, 1)
	select {
	case waitErr := <-failure:
		t.Fatalf("WaitOperation: %v", waitErr)
	case update := <-result:
		if !update.Changed || update.Operation.Status != OperationDelivered ||
			update.Operation.Terminal || update.Operation.ExecutionConfirmed {
			t.Fatalf("WaitOperation = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitOperation did not wake after delivery")
	}
}

func TestCancellationDistinguishesQueuedAndDeliveredWork(t *testing.T) {
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	queued, err := service.SubmitAction(
		context.Background(), principal,
		actionHost.input("request.cancel.queued", "action.cancel.queued"),
	)
	if err != nil {
		t.Fatalf("SubmitAction queued: %v", err)
	}
	queued, err = service.CancelOperation(principal, queued.OperationID)
	if err != nil || queued.Status != OperationCancelled || !queued.Terminal {
		t.Fatalf("queued cancellation = %#v, %v", queued, err)
	}
	delivered, err := service.SubmitAction(
		context.Background(), principal,
		actionHost.input("request.cancel.delivered", "action.cancel.delivered"),
	)
	if err != nil {
		t.Fatalf("SubmitAction delivered: %v", err)
	}
	pollHost(t, service, lease, 1)
	delivered, err = service.CancelOperation(principal, delivered.OperationID)
	if err != nil || !delivered.CancelRequested || delivered.Terminal {
		t.Fatalf("delivered cancellation = %#v, %v", delivered, err)
	}
	batch := pollHost(t, service, lease, 1)
	if len(batch.Cancellations) != 1 ||
		batch.Cancellations[0] != delivered.OperationID {
		t.Fatalf("Host cancellation batch = %#v", batch)
	}
}

func TestLeaseExpiryDistinguishesUnacceptedAndAcceptedWork(t *testing.T) {
	t.Run("unaccepted", func(t *testing.T) {
		service, _, now, principal, actionHost := actionOperationTestHarness(t, Options{})
		operation, err := service.SubmitAction(
			context.Background(), principal,
			actionHost.input("request.expiry.unaccepted", "action.expiry.unaccepted"),
		)
		if err != nil {
			t.Fatalf("SubmitAction: %v", err)
		}
		*now = now.Add(6 * time.Second)
		view, err := service.GetOperation(principal, operation.OperationID)
		if err != nil || view.Status != OperationStale || !view.Terminal ||
			view.ExecutionConfirmed {
			t.Fatalf("expired unaccepted operation = %#v, %v", view, err)
		}
	})
	t.Run("accepted", func(t *testing.T) {
		service, lease, now, principal, actionHost := actionOperationTestHarness(t, Options{})
		operation, err := service.SubmitAction(
			context.Background(), principal,
			actionHost.input("request.expiry.accepted", "action.expiry.accepted"),
		)
		if err != nil {
			t.Fatalf("SubmitAction: %v", err)
		}
		pollHost(t, service, lease, 1)
		if err := service.AcknowledgeHost(
			"test.host", lease.LeaseID,
			HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
		); err != nil {
			t.Fatalf("AcknowledgeHost: %v", err)
		}
		*now = now.Add(6 * time.Second)
		view, err := service.GetOperation(principal, operation.OperationID)
		if err != nil || view.Status != OperationOutcomeUnknown || view.Terminal ||
			!view.ReconciliationPending || view.ExecutionConfirmed {
			t.Fatalf("expired accepted operation = %#v, %v", view, err)
		}
	})
}

func TestOutcomeUnknownAcceptsLateAuthoritativeReconciliation(t *testing.T) {
	service, lease, now, principal, actionHost := actionOperationTestHarness(t, Options{})
	operation, err := service.SubmitAction(
		context.Background(), principal,
		actionHost.input("request.reconcile.late", "action.reconcile.late"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host", lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	*now = now.Add(6 * time.Second)
	if _, err := service.GetOperation(principal, operation.OperationID); err != nil {
		t.Fatalf("GetOperation after expiry: %v", err)
	}
	replacement, err := service.RegisterHost(registration("instance.reconcile"))
	if err != nil {
		t.Fatalf("RegisterHost replacement: %v", err)
	}
	if err := service.ReportHostOutcome(
		"test.host", replacement.LeaseID,
		host.ActionOutcome{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			Summary:     "The original Host reconciled its result.",
			Epoch:       testEpoch(),
			WorldSeq:    2,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 12},
		},
	); err != nil {
		t.Fatalf("ReportHostOutcome: %v", err)
	}
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationSucceeded || !view.Terminal ||
		!view.ExecutionConfirmed || view.ReconciliationPending {
		t.Fatalf("reconciled operation = %#v, %v", view, err)
	}
}

func TestHostPollWakesWhenActionArrives(t *testing.T) {
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan HostControlBatch, 1)
	failure := make(chan error, 1)
	go func() {
		batch, err := service.PollHost(ctx, "test.host", lease.LeaseID, 1)
		if err != nil {
			failure <- err
			return
		}
		result <- batch
	}()
	time.Sleep(10 * time.Millisecond)
	operation, err := service.SubmitAction(
		context.Background(), principal,
		actionHost.input("request.poll.wake", "action.poll.wake"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	select {
	case err := <-failure:
		t.Fatalf("PollHost: %v", err)
	case batch := <-result:
		if len(batch.Requests) != 1 ||
			batch.Requests[0].Request.OperationID != operation.OperationID {
			t.Fatalf("poll batch = %#v", batch)
		}
	case <-ctx.Done():
		t.Fatal("PollHost did not wake after action submission")
	}
}

func TestListOperationsFiltersPrincipalVisibleHistory(t *testing.T) {
	service, _, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	input := actionHost.input("request.operation.list", "action.operation.list")
	input.Request.TaskID = "task.operation.list"
	operation, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListOperations(principal, ListOperationsInput{
		TaskID: input.Request.TaskID, Status: OperationQueued, Limit: 10,
	})
	if err != nil || len(listed) != 1 || listed[0].OperationID != operation.OperationID {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	outsider := operationPrincipal(ScopeActorRead)
	outsider.ID = "player.other"
	listed, err = service.ListOperations(outsider, ListOperationsInput{Limit: 10})
	if err != nil || len(listed) != 0 {
		t.Fatalf("outsider listed = %#v, %v", listed, err)
	}
	admin := operationPrincipal(ScopeHostAdmin)
	admin.ID = "rin.console"
	listed, err = service.ListOperations(admin, ListOperationsInput{ActorID: "actor.one"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("admin listed = %#v, %v", listed, err)
	}
}

func actionOperationTestHarness(
	t *testing.T,
	options Options,
) (*Service, HostLease, *time.Time, host.Principal, *actionGatewayHost) {
	t.Helper()
	actionHost, engine := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	options.ActionHost = actionHost
	options.PolicyEngine = engine
	service, lease, now := operationTestService(t, options)
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorControl,
		ScopeActorExecute,
		ScopeOperationCancel,
	)
	if _, err := service.AcquireController(principal, AcquireControllerInput{
		ActorControlTarget: testActorControlTarget(),
		ControllerID:       "controller.gateway.one",
		LeaseTTLMillis:     5_000,
	}); err != nil {
		t.Fatalf("AcquireController: %v", err)
	}
	return service, lease, now, principal, actionHost
}

func operationTestService(
	t *testing.T,
	options Options,
) (*Service, HostLease, *time.Time) {
	t.Helper()
	now := time.UnixMilli(1_000_000)
	random := make([]byte, 4_096)
	for index := range random {
		random[index] = byte(index)
	}
	options.Now = func() time.Time { return now }
	options.Random = bytes.NewReader(random)
	service := New(options)
	t.Cleanup(func() { _ = service.Close() })
	lease := mustRegister(t, service, registration("instance.operation"))
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		worldPublication(1, "ready"),
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	return service, lease, &now
}

func operationPrincipal(scopes ...string) host.Principal {
	return host.Principal{
		ID:            "player.one",
		GrantedScopes: scopes,
	}
}

func pollHost(
	t *testing.T,
	service *Service,
	lease HostLease,
	limit int,
) HostControlBatch {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch, err := service.PollHost(ctx, "test.host", lease.LeaseID, limit)
	if err != nil {
		t.Fatalf("PollHost: %v", err)
	}
	return batch
}

func testEpoch() host.Epoch {
	return host.Epoch{
		SessionID: "session.one",
		WorldID:   "world.one",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
}

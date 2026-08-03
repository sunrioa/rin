package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
)

func TestMessageOperationIsIdempotentAndReportsHostOutcome(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorConverse,
		ScopeOperationCancel,
	)
	input := ActorTextInput{
		RequestID: "request.message.one",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Are you ready?",
	}
	operation, err := service.SendActorMessage(principal, input)
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	if operation.Cursor == "" || operation.Terminal ||
		operation.ExecutionConfirmed {
		t.Fatalf("queued operation semantics = %#v", operation)
	}
	retried, err := service.SendActorMessage(principal, input)
	if err != nil || retried.OperationID != operation.OperationID {
		t.Fatalf("idempotent retry = %#v, %v", retried, err)
	}
	reordered := operationPrincipal(
		ScopeOperationCancel,
		ScopeActorConverse,
		ScopeActorRead,
	)
	retried, err = service.SendActorMessage(reordered, input)
	if err != nil || retried.OperationID != operation.OperationID {
		t.Fatalf("reordered-scope retry = %#v, %v", retried, err)
	}
	changed := input
	changed.Text = "Do something else."
	if _, err := service.SendActorMessage(
		principal,
		changed,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry error = %v", err)
	}

	batch := pollHost(t, service, lease, 8)
	if len(batch.Requests) != 1 {
		t.Fatalf("first batch = %#v", batch)
	}
	binding := batch.Requests[0].Request.Binding
	if batch.Requests[0].Request.OperationID != operation.OperationID ||
		batch.Requests[0].Request.Text != input.Text ||
		batch.Requests[0].Request.Principal.ID != principal.ID ||
		binding == nil ||
		binding.Epoch != testEpoch() ||
		binding.ObservationSeq != 1 ||
		batch.Requests[0].DeliveryAttempt != 1 {
		t.Fatalf("first batch = %#v", batch)
	}
	redelivery := pollHost(t, service, lease, 8)
	if len(redelivery.Requests) != 1 ||
		redelivery.Requests[0].DeliveryAttempt != 2 {
		t.Fatalf("redelivery = %#v", redelivery)
	}

	ack := HostAcknowledgement{
		OperationID: operation.OperationID,
		Accepted:    true,
		Message:     "queued on authority thread",
	}
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, ack); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, ack); err != nil {
		t.Fatalf("idempotent AcknowledgeHost: %v", err)
	}
	acceptedRedelivery := pollHost(t, service, lease, 8)
	if len(acceptedRedelivery.Requests) != 1 ||
		acceptedRedelivery.Requests[0].Request.OperationID != operation.OperationID ||
		acceptedRedelivery.Requests[0].DeliveryAttempt != 3 {
		t.Fatalf("accepted redelivery = %#v", acceptedRedelivery)
	}
	acceptedView, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || acceptedView.Status != OperationAccepted {
		t.Fatalf("accepted redelivery changed operation state = %#v, %v", acceptedView, err)
	}
	if err := service.ReportHostRun(
		"test.host",
		lease.LeaseID,
		host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionQueued,
			ProgressSeq: 1,
			Progress:    0,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 2},
		},
	); err != nil {
		t.Fatalf("queued ReportHostRun: %v", err)
	}
	if err := service.ReportHostRun(
		"test.host",
		lease.LeaseID,
		host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionRunning,
			ProgressSeq: 2,
			Progress:    50,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 3},
		},
	); err != nil {
		t.Fatalf("running ReportHostRun: %v", err)
	}
	if err := service.ReportHostRun(
		"test.host",
		lease.LeaseID,
		host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionRunning,
			ProgressSeq: 3,
			Progress:    75,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 3},
		},
	); err != nil {
		t.Fatalf("same-status ReportHostRun: %v", err)
	}
	outcome := host.ActionOutcome{
		OperationID: operation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     "The companion replied.",
		Epoch:       testEpoch(),
		WorldSeq:    2,
		OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 4},
	}
	mismatchedEpoch := outcome
	mismatchedEpoch.Epoch.Timeline++
	if err := service.ReportHostOutcome(
		"test.host",
		lease.LeaseID,
		mismatchedEpoch,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched outcome epoch error = %v", err)
	}
	output := json.RawMessage(
		`{"type":"actor_turn","reply":"I am ready.","selected_offer_id":"offer.wait"}`,
	)
	if err := service.ReportHostResult(
		"test.host",
		lease.LeaseID,
		outcome,
		json.RawMessage(`{"reply":"first","reply":"second"}`),
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid ReportHostResult error = %v", err)
	}
	if err := service.ReportHostResult(
		"test.host",
		lease.LeaseID,
		outcome,
		output,
	); err != nil {
		t.Fatalf("ReportHostResult: %v", err)
	}
	if err := service.ReportHostResult(
		"test.host",
		lease.LeaseID,
		outcome,
		output,
	); err != nil {
		t.Fatalf("idempotent ReportHostResult: %v", err)
	}
	if err := service.ReportHostOutcome(
		"test.host",
		lease.LeaseID,
		outcome,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed terminal output error = %v", err)
	}
	if err := service.ReportHostRun(
		"test.host",
		lease.LeaseID,
		host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			ProgressSeq: 4,
			Progress:    100,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 5},
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("run after terminal outcome error = %v", err)
	}

	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil ||
		view.Status != OperationSucceeded ||
		!view.Terminal ||
		!view.ExecutionConfirmed ||
		view.DeliveryAttempts != 3 ||
		view.Run == nil ||
		view.Run.Status != host.ActionRunning ||
		view.Outcome == nil ||
		view.Outcome.Summary != outcome.Summary ||
		view.Output["reply"] != "I am ready." ||
		view.Output["selected_offer_id"] != "offer.wait" {
		t.Fatalf("GetOperation = %#v, %v", view, err)
	}
}

func TestHostOutcomeCannotPredateBoundObservation(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorConverse)
	publication := worldPublication(2, "newer")
	publication.Actors[0].ObservationSeq = 2
	publication.Actors[0].Offers[0].ObservationSeq = 2
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.outcome.world.sequence",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Use the newer observation.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.ReportHostOutcome(
		"test.host",
		lease.LeaseID,
		host.ActionOutcome{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			Summary:     "This result claims an older world state.",
			Epoch:       testEpoch(),
			WorldSeq:    1,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 3},
		},
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("older outcome world sequence error = %v", err)
	}
}

func TestFreshHostWorkIsNotStarvedByAcceptedRedelivery(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorConverse)
	first, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.fairness.first",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "First request.",
	})
	if err != nil {
		t.Fatalf("first SendActorMessage: %v", err)
	}
	batch := pollHost(t, service, lease, 1)
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != first.OperationID {
		t.Fatalf("first poll = %#v", batch)
	}
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, HostAcknowledgement{
		OperationID: first.OperationID,
		Accepted:    true,
	}); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	second, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.fairness.second",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Second request.",
	})
	if err != nil {
		t.Fatalf("second SendActorMessage: %v", err)
	}
	batch = pollHost(t, service, lease, 1)
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != second.OperationID {
		t.Fatalf("fresh work was starved by accepted redelivery: %#v", batch)
	}
}

func TestWaitOperationWakesForDeliveryWithoutClaimingExecution(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.wait.delivery",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Wait for the Host.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}

	result := make(chan OperationUpdate, 1)
	failure := make(chan error, 1)
	go func() {
		update, waitErr := service.WaitOperation(
			context.Background(),
			principal,
			WaitOperationInput{
				OperationID: operation.OperationID,
				AfterCursor: operation.Cursor,
				WaitMillis:  1_000,
			},
		)
		if waitErr != nil {
			failure <- waitErr
			return
		}
		result <- update
	}()

	pollHost(t, service, lease, 1)
	select {
	case waitErr := <-failure:
		t.Fatalf("WaitOperation: %v", waitErr)
	case update := <-result:
		if !update.Changed ||
			update.Operation.Status != OperationDelivered ||
			update.Operation.DeliveryAttempts != 1 ||
			update.Operation.Terminal ||
			update.Operation.ExecutionConfirmed {
			t.Fatalf("WaitOperation = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitOperation did not wake after Host delivery")
	}

	if _, err := service.WaitOperation(
		context.Background(),
		principal,
		WaitOperationInput{OperationID: operation.OperationID, WaitMillis: 25_001},
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized WaitOperation error = %v", err)
	}
}

func TestExecuteOfferDeliversExactHostOfferAndBinding(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorExecute)
	input := ExecuteOfferInput{
		RequestID: "request.offer.one",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		OfferID:   "offer.follow",
	}
	operation, err := service.ExecuteActorOffer(principal, input)
	if err != nil {
		t.Fatalf("ExecuteActorOffer: %v", err)
	}
	batch := pollHost(t, service, lease, 1)
	if len(batch.Requests) != 1 {
		t.Fatalf("batch = %#v", batch)
	}
	request := batch.Requests[0].Request
	offer := request.Offer
	binding := request.Binding
	expectedOffer := worldPublication(1, "ready").Actors[0].Offers[0]
	if request.OperationID != operation.OperationID ||
		request.Invocation != nil ||
		offer == nil ||
		binding == nil ||
		offer.OfferID != expectedOffer.OfferID ||
		offer.Capability != expectedOffer.Capability ||
		offer.DescriptorDigest != expectedOffer.DescriptorDigest ||
		!bytes.Equal(offer.Arguments, expectedOffer.Arguments) ||
		offer.ExpectedEpoch != binding.Epoch ||
		offer.ObservationSeq != binding.ObservationSeq {
		t.Fatalf("request = %#v", request)
	}

	notOffered := input
	notOffered.RequestID = "request.offer.missing"
	notOffered.OfferID = "offer.not-published"
	if _, err := service.ExecuteActorOffer(
		principal,
		notOffered,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbound offer error = %v", err)
	}
	stranger := host.Principal{
		ID:            "player.two",
		GrantedScopes: []string{ScopeActorExecute},
	}
	foreign := input
	foreign.RequestID = "request.offer.foreign"
	if _, err := service.ExecuteActorOffer(
		stranger,
		foreign,
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign actor error = %v", err)
	}
}

func TestCancellationDistinguishesQueuedAndDeliveredWork(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(
		ScopeActorDirect,
		ScopeOperationCancel,
	)
	queued, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.directive.queued",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Wait by the door.",
	})
	if err != nil {
		t.Fatalf("SendActorDirective queued: %v", err)
	}
	cancelled, err := service.CancelOperation(principal, queued.OperationID)
	if err != nil || cancelled.Status != OperationCancelled ||
		cancelled.CancelRequested {
		t.Fatalf("cancel queued = %#v, %v", cancelled, err)
	}

	delivered, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.directive.delivered",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Check the courtyard.",
	})
	if err != nil {
		t.Fatalf("SendActorDirective delivered: %v", err)
	}
	if batch := pollHost(t, service, lease, 8); len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != delivered.OperationID {
		t.Fatalf("delivery batch = %#v", batch)
	}
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{
			OperationID: delivered.OperationID,
			Accepted:    true,
		},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	cancelling, err := service.CancelOperation(principal, delivered.OperationID)
	if err != nil || cancelling.Status != OperationAccepted ||
		!cancelling.CancelRequested {
		t.Fatalf("cancel delivered = %#v, %v", cancelling, err)
	}
	batch := pollHost(t, service, lease, 8)
	if len(batch.Requests) != 0 ||
		!reflect.DeepEqual(batch.Cancellations, []string{delivered.OperationID}) {
		t.Fatalf("cancellation batch = %#v", batch)
	}
	if err := service.ReportHostOutcome(
		"test.host",
		lease.LeaseID,
		host.ActionOutcome{
			OperationID: delivered.OperationID,
			Status:      host.ActionCancelled,
			Code:        "control.cancelled",
			Summary:     "The Host cancelled the task before mutation.",
			Epoch:       testEpoch(),
			WorldSeq:    2,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 4},
		},
	); err != nil {
		t.Fatalf("cancel outcome: %v", err)
	}
	finished, err := service.GetOperation(principal, delivered.OperationID)
	if err != nil || finished.Status != OperationCancelled ||
		finished.Outcome == nil {
		t.Fatalf("cancelled operation = %#v, %v", finished, err)
	}
}

func TestLeaseExpiryMakesUndeliveredWorkStaleAndAcceptedWorkUnknown(
	t *testing.T,
) {
	service, lease, now := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorConverse)
	stale, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.expire.stale",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "This should never be delivered.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage stale: %v", err)
	}
	unknown, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.expire.unknown",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "This may already be running.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage unknown: %v", err)
	}
	batch := pollHost(t, service, lease, 1)
	deliveredID := batch.Requests[0].Request.OperationID
	if deliveredID != stale.OperationID && deliveredID != unknown.OperationID {
		t.Fatalf("unexpected delivered operation %q", deliveredID)
	}
	undeliveredID := stale.OperationID
	if deliveredID == stale.OperationID {
		undeliveredID = unknown.OperationID
	}
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: deliveredID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.ReportHostRun(
		"test.host",
		lease.LeaseID,
		host.ActionRun{
			OperationID: deliveredID,
			Status:      host.ActionSucceeded,
			ProgressSeq: 1,
			Progress:    100,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 2},
		},
	); err != nil {
		t.Fatalf("terminal ReportHostRun: %v", err)
	}

	*now = now.Add(6 * time.Second)
	deliveredView, err := service.GetOperation(principal, deliveredID)
	if err != nil || deliveredView.Status != OperationOutcomeUnknown ||
		deliveredView.Terminal || !deliveredView.ReconciliationPending ||
		deliveredView.ExecutionConfirmed {
		t.Fatalf("accepted after expiry = %#v, %v", deliveredView, err)
	}
	undeliveredView, err := service.GetOperation(principal, undeliveredID)
	if err != nil || undeliveredView.Status != OperationStale ||
		undeliveredView.DeliveryAttempts != 0 ||
		!undeliveredView.Terminal || undeliveredView.ReconciliationPending ||
		undeliveredView.ExecutionConfirmed {
		t.Fatalf("queued after expiry = %#v, %v", undeliveredView, err)
	}
}

func TestLeaseExpiryRedeliversAcceptedWorkWithoutExecutionEvidence(
	t *testing.T,
) {
	service, lease, now := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.expire.accepted.redelivery",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Resume after the Host lease is replaced.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}

	*now = now.Add(6 * time.Second)
	recoveryLease := registerAndPublishOperationHost(
		t,
		service,
		"instance.accepted.redelivery",
	)
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationAccepted || view.Terminal ||
		view.ReconciliationPending {
		t.Fatalf("accepted operation after lease replacement = %#v, %v", view, err)
	}
	batch := pollHost(t, service, recoveryLease, 1)
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != operation.OperationID ||
		batch.Requests[0].DeliveryAttempt != 2 {
		t.Fatalf("accepted lease-recovery delivery = %#v", batch)
	}
}

func TestOutcomeUnknownWaitsForLateAuthoritativeReconciliation(t *testing.T) {
	service, lease, now := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.reconcile.late",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Reconcile this from the Host outbox.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.ReportHostRun(
		"test.host",
		lease.LeaseID,
		host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionRunning,
			ProgressSeq: 1,
			Progress:    10,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 2},
		},
	); err != nil {
		t.Fatalf("ReportHostRun: %v", err)
	}

	*now = now.Add(6 * time.Second)
	unknown, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || unknown.Terminal || !unknown.ReconciliationPending {
		t.Fatalf("unknown operation = %#v, %v", unknown, err)
	}
	waitResult := make(chan OperationUpdate, 1)
	waitFailure := make(chan error, 1)
	go func() {
		update, waitErr := service.WaitOperation(
			context.Background(),
			principal,
			WaitOperationInput{
				OperationID: operation.OperationID,
				AfterCursor: unknown.Cursor,
				WaitMillis:  1_000,
			},
		)
		if waitErr != nil {
			waitFailure <- waitErr
			return
		}
		waitResult <- update
	}()

	recoveryLease := registerAndPublishOperationHost(
		t,
		service,
		"instance.reconciler",
	)
	outcome := host.ActionOutcome{
		OperationID: operation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     "Recovered from the Host outbox.",
		Epoch:       testEpoch(),
		WorldSeq:    2,
		OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 3},
	}
	if err := service.ReportHostOutcome(
		"test.host",
		recoveryLease.LeaseID,
		outcome,
	); err != nil {
		t.Fatalf("ReportHostOutcome: %v", err)
	}
	select {
	case waitErr := <-waitFailure:
		t.Fatalf("WaitOperation: %v", waitErr)
	case update := <-waitResult:
		if !update.Changed || !update.Operation.Terminal ||
			update.Operation.ReconciliationPending ||
			!update.Operation.ExecutionConfirmed {
			t.Fatalf("reconciled update = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitOperation did not wake for late reconciliation")
	}
}

func TestAuthoritativeOutcomeUnknownSettlesWithoutConfirmingExecution(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.outcome.unknown.authoritative",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Preserve uncertainty honestly.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	outcome := host.ActionOutcome{
		OperationID: operation.OperationID,
		Status:      host.ActionOutcomeUnknown,
		Code:        "host.outcome_unknown",
		Summary:     "The Host cannot prove whether the effect completed.",
		Epoch:       testEpoch(),
		WorldSeq:    2,
		OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 3},
	}
	if err := service.ReportHostOutcome(
		"test.host",
		lease.LeaseID,
		outcome,
	); err != nil {
		t.Fatalf("ReportHostOutcome: %v", err)
	}
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationOutcomeUnknown || !view.Terminal ||
		view.ReconciliationPending || view.ExecutionConfirmed || view.Outcome == nil ||
		view.Outcome.Status != host.ActionOutcomeUnknown {
		t.Fatalf("authoritative outcome-unknown view = %#v, %v", view, err)
	}
}

func TestUnreconciledOutcomeUnknownExpiresFromCapacity(t *testing.T) {
	service, lease, now := operationTestService(t, Options{
		MaxOperations: 1,
		OperationTTL:  time.Second,
	})
	principal := operationPrincipal(ScopeActorConverse)
	first, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.unknown.capacity.one",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "This result may be lost.",
	})
	if err != nil {
		t.Fatalf("first SendActorMessage: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: first.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}

	*now = now.Add(2 * time.Second)
	unknown, err := service.GetOperation(principal, first.OperationID)
	if err != nil || unknown.Status != OperationOutcomeUnknown ||
		!unknown.ReconciliationPending {
		t.Fatalf("expired accepted operation = %#v, %v", unknown, err)
	}
	*now = now.Add(2 * time.Second)
	second, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.unknown.capacity.two",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Capacity must recover after reconciliation retention.",
	})
	if err != nil || second.OperationID == "" {
		t.Fatalf("second SendActorMessage = %#v, %v", second, err)
	}
	if _, err := service.GetOperation(
		principal,
		first.OperationID,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned unknown operation error = %v", err)
	}
}

func TestOperationCapacityPrunesExpiredCompletedWork(t *testing.T) {
	service, _, now := operationTestService(t, Options{
		MaxOperations: 1,
		OperationTTL:  time.Second,
	})
	principal := operationPrincipal(
		ScopeActorDirect,
		ScopeOperationCancel,
	)
	first, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.capacity.one",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "First task.",
	})
	if err != nil {
		t.Fatalf("first directive: %v", err)
	}
	second := ActorTextInput{
		RequestID: "request.capacity.two",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Second task.",
	}
	if _, err := service.SendActorDirective(
		principal,
		second,
	); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	if _, err := service.CancelOperation(principal, first.OperationID); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	*now = now.Add(2 * time.Second)
	if _, err := service.SendActorDirective(principal, second); err != nil {
		t.Fatalf("directive after retention expiry: %v", err)
	}
}

func TestOrphanedOperationExpiresWithoutRepublishedHost(t *testing.T) {
	service, _, now := operationTestService(t, Options{
		OperationTTL: time.Second,
	})
	principal := operationPrincipal(ScopeActorDirect)
	operation, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.orphan.expires",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Do not survive an abandoned control plane.",
	})
	if err != nil {
		t.Fatalf("SendActorDirective: %v", err)
	}
	service.mu.Lock()
	delete(service.hosts, "test.host")
	service.mu.Unlock()
	*now = now.Add(2 * time.Second)

	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	if view.Status != OperationStale {
		t.Fatalf("orphaned operation status = %s", view.Status)
	}
}

func TestHostPollWakesWhenWorkArrives(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan HostControlBatch, 1)
	failure := make(chan error, 1)
	go func() {
		batch, err := service.PollHost(
			ctx,
			"test.host",
			lease.LeaseID,
			1,
		)
		if err != nil {
			failure <- err
			return
		}
		result <- batch
	}()

	operation, err := service.SendActorMessage(
		operationPrincipal(ScopeActorConverse),
		ActorTextInput{
			RequestID: "request.poll.wake",
			HostID:    "test.host",
			WorldID:   "world.one",
			ActorID:   "actor.one",
			Text:      "Wake up.",
		},
	)
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
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
		t.Fatal("PollHost did not wake after submission")
	}
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
	batch, err := service.PollHost(
		ctx,
		"test.host",
		lease.LeaseID,
		limit,
	)
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

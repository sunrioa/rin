package controlplane

import (
	"context"
	"errors"
	"testing"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestControllerLeaseIsExclusiveAndEpochBound(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorRead, ScopeActorControl)
	input := AcquireControllerInput{
		ActorControlTarget: testActorControlTarget(),
		ControllerID:       "controller.external.one",
		LeaseTTLMillis:     5_000,
	}
	first, err := service.AcquireController(principal, input)
	if err != nil {
		t.Fatalf("AcquireController: %v", err)
	}
	second, err := service.AcquireController(principal, input)
	if err != nil || second.LeaseID != first.LeaseID ||
		second.ExpiresAtUnixMillis != first.ExpiresAtUnixMillis {
		t.Fatalf("idempotent acquire = %#v, %v", second, err)
	}
	competing := input
	competing.ControllerID = "controller.external.two"
	if _, err := service.AcquireController(
		principal,
		competing,
	); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("competing controller error = %v", err)
	}

	view, err := service.GetActor(
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
	)
	if err != nil || view.Controller == nil ||
		view.Controller.LeaseID != first.LeaseID {
		t.Fatalf("actor controller = %#v, %v", view.Controller, err)
	}
	update, err := service.WaitActor(
		context.Background(),
		principal,
		WaitActorInput{
			HostID:                 input.HostID,
			WorldID:                input.WorldID,
			ActorID:                input.ActorID,
			AfterObservationSeq:    view.ObservationSeq,
			AfterAuthorityRevision: view.Authority.Revision,
		},
	)
	if err != nil || !update.Changed {
		t.Fatalf("WaitActor controller update = %#v, %v", update, err)
	}

	publication := worldPublication(2, "internal")
	publication.Actors[0].Authority = &DecisionAuthority{
		Source:      DecisionInternal,
		Revision:    2,
		PersonaMode: PersonaCharacterBound,
	}
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	if _, err := service.GetController(
		principal,
		input.ActorControlTarget,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("superseded controller error = %v", err)
	}
}

func TestInternalControllerRequiresHostAdministrator(t *testing.T) {
	service, hostLease, _ := operationTestService(t, Options{})
	publication := worldPublication(2, "internal")
	publication.Actors[0].Authority = &DecisionAuthority{
		Source:      DecisionInternal,
		Revision:    2,
		PersonaMode: PersonaCharacterBound,
	}
	if err := service.PublishWorld(
		"test.host",
		hostLease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	input := AcquireControllerInput{
		ActorControlTarget: testActorControlTarget(),
		ControllerID:       "controller.internal.one",
		LeaseTTLMillis:     5_000,
	}
	owner := operationPrincipal(ScopeActorControl)
	if _, err := service.AcquireController(
		owner,
		input,
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner internal acquire error = %v", err)
	}
	admin := host.Principal{
		ID:            "host.runtime",
		GrantedScopes: []string{ScopeHostAdmin},
	}
	controller, err := service.AcquireController(admin, input)
	if err != nil || controller.Source != DecisionInternal ||
		controller.PrincipalID != admin.ID {
		t.Fatalf("internal controller = %#v, %v", controller, err)
	}
}

func TestEmergencyStopCancelsQueuedAndDeliveredActorWork(t *testing.T) {
	service, hostLease, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	queued, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.stop.queued", "action.stop.queued"),
	)
	if err != nil {
		t.Fatalf("SubmitAction queued: %v", err)
	}
	stop, err := service.SetActorEmergencyStop(
		principal,
		testActorControlTarget(),
		true,
	)
	if err != nil || !stop.Active || stop.Revision != 1 {
		t.Fatalf("SetActorEmergencyStop = %#v, %v", stop, err)
	}
	view, err := service.GetOperation(principal, queued.OperationID)
	if err != nil || view.Status != OperationCancelled || !view.Terminal ||
		view.ExecutionConfirmed {
		t.Fatalf("queued stop result = %#v, %v", view, err)
	}
	if _, err := service.SetActorEmergencyStop(
		principal,
		testActorControlTarget(),
		false,
	); err != nil {
		t.Fatalf("clear emergency stop: %v", err)
	}

	delivered, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.stop.delivered", "action.stop.delivered"),
	)
	if err != nil {
		t.Fatalf("SubmitAction delivered: %v", err)
	}
	batch := pollHost(t, service, hostLease, 1)
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != delivered.OperationID {
		t.Fatalf("delivery batch = %#v", batch)
	}
	if _, err := service.SetActorEmergencyStop(
		principal,
		testActorControlTarget(),
		true,
	); err != nil {
		t.Fatalf("second emergency stop: %v", err)
	}
	view, err = service.GetOperation(principal, delivered.OperationID)
	if err != nil || !view.CancelRequested || view.Terminal ||
		view.ExecutionConfirmed {
		t.Fatalf("delivered stop result = %#v, %v", view, err)
	}
	batch = pollHost(t, service, hostLease, 1)
	if len(batch.Cancellations) != 1 ||
		batch.Cancellations[0] != delivered.OperationID {
		t.Fatalf("cancellation batch = %#v", batch)
	}
}

func TestOwnerCanLatchEmergencyStopWhileHostIsOffline(t *testing.T) {
	service, _, now := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorRead, ScopeActorControl)
	*now = now.Add(6_000_000_000)
	stop, err := service.SetActorEmergencyStop(
		principal,
		testActorControlTarget(),
		true,
	)
	if err != nil || !stop.Active {
		t.Fatalf("offline emergency stop = %#v, %v", stop, err)
	}
	view, err := service.GetActor(
		principal,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || !view.EmergencyStopped || view.Online {
		t.Fatalf("offline stopped actor = %#v, %v", view, err)
	}
}

func TestControllerLeaseExpiresBeforeAnotherControllerAcquires(t *testing.T) {
	service, hostLease, now := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorRead, ScopeActorControl)
	input := AcquireControllerInput{
		ActorControlTarget: testActorControlTarget(),
		ControllerID:       "controller.external.one",
		LeaseTTLMillis:     5_000,
	}
	first, err := service.AcquireController(principal, input)
	if err != nil {
		t.Fatalf("AcquireController: %v", err)
	}
	*now = now.Add(4_000_000_000)
	if _, err := service.RenewHost("test.host", hostLease.LeaseID); err != nil {
		t.Fatalf("RenewHost: %v", err)
	}
	*now = now.Add(1_001_000_000)
	input.ControllerID = "controller.external.two"
	second, err := service.AcquireController(principal, input)
	if err != nil || second.LeaseID == first.LeaseID {
		t.Fatalf("replacement controller = %#v, %v", second, err)
	}
	if _, err := service.RenewController(
		principal,
		input.ActorControlTarget,
		first.LeaseID,
		5_000,
	); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired renewal error = %v", err)
	}
}

func testActorControlTarget() ActorControlTarget {
	return ActorControlTarget{
		HostID:  "test.host",
		WorldID: "world.one",
		ActorID: "actor.one",
	}
}

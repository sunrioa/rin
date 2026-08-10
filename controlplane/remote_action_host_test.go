package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestPollingActionHostCompletesGatewayBeforeQueuingOperation(t *testing.T) {
	binder, engine := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	service, lease, _ := operationTestService(t, Options{PolicyEngine: engine})
	principal := acquireRemoteActionController(t, service)

	type submissionResult struct {
		operation OperationView
		err       error
	}
	submitted := make(chan submissionResult, 1)
	go func() {
		operation, err := service.SubmitAction(
			context.Background(),
			principal,
			binder.input("request.remote.bind", "action.remote.bind"),
		)
		submitted <- submissionResult{operation: operation, err: err}
	}()

	first := pollHost(t, service, lease, 1)
	if len(first.GatewayRequests) != 1 || len(first.Requests) != 0 {
		t.Fatalf("first PollHost = %#v", first)
	}
	delivery := first.GatewayRequests[0]
	if delivery.DeliveryAttempt != 1 || delivery.Request.Kind != HostGatewayBind ||
		delivery.Request.ActionRequest == nil {
		t.Fatalf("binding delivery = %#v", delivery)
	}

	redelivery := pollHost(t, service, lease, 1)
	if len(redelivery.GatewayRequests) != 1 ||
		redelivery.GatewayRequests[0].Request.GatewayRequestID !=
			delivery.Request.GatewayRequestID ||
		redelivery.GatewayRequests[0].DeliveryAttempt != 2 {
		t.Fatalf("binding redelivery = %#v", redelivery)
	}
	binding, err := binder.BindAction(
		context.Background(),
		delivery.Request.Target,
		*delivery.Request.ActionRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostGatewayResult(
		"test.host",
		lease.LeaseID,
		HostGatewayResult{
			GatewayRequestID: delivery.Request.GatewayRequestID,
			Binding:          &binding,
		},
	); err != nil {
		t.Fatalf("ReportHostGatewayResult: %v", err)
	}

	select {
	case result := <-submitted:
		if result.err != nil || result.operation.Status != OperationQueued ||
			result.operation.BoundAction == nil ||
			result.operation.PolicyDecision == nil {
			t.Fatalf("SubmitAction = %#v, %v", result.operation, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("SubmitAction did not resume after Host binding")
	}

	work := pollHost(t, service, lease, 1)
	if len(work.GatewayRequests) != 0 || len(work.Requests) != 1 ||
		work.Requests[0].Request.Kind != ControlAction {
		t.Fatalf("operation delivery = %#v", work)
	}
}

func TestPollingActionHostConfirmationUsesFreshHostSnapshot(t *testing.T) {
	binder, engine := actionGatewayTestComponents(t, host.RiskCritical, policy.ProfileOpen)
	service, lease, _ := operationTestService(t, Options{PolicyEngine: engine})
	principal := acquireRemoteActionController(t, service)
	principal.GrantedScopes = append(principal.GrantedScopes, "rin.policy.confirm")

	pending := submitRemoteBoundAction(
		t,
		service,
		lease,
		principal,
		binder,
		"request.remote.confirm",
		"action.remote.confirm",
	)
	if pending.Status != OperationAwaitingConfirmation ||
		pending.PolicyDecision == nil ||
		pending.PolicyDecision.Confirmation == nil {
		t.Fatalf("pending confirmation = %#v", pending)
	}

	type confirmationResult struct {
		operation OperationView
		err       error
	}
	confirmed := make(chan confirmationResult, 1)
	go func() {
		operation, err := service.ConfirmAction(
			context.Background(), principal, pending.OperationID,
		)
		confirmed <- confirmationResult{operation: operation, err: err}
	}()
	delivery := pollHost(t, service, lease, 1)
	if len(delivery.GatewayRequests) != 1 ||
		delivery.GatewayRequests[0].Request.Kind != HostGatewaySnapshot ||
		delivery.GatewayRequests[0].Request.ActionRequest != nil {
		t.Fatalf("snapshot delivery = %#v", delivery)
	}
	requestID := delivery.GatewayRequests[0].Request.GatewayRequestID
	snapshot := binder.snapshot
	if err := service.ReportHostGatewayResult(
		"test.host",
		lease.LeaseID,
		HostGatewayResult{
			GatewayRequestID: requestID,
			Snapshot:         &snapshot,
		},
	); err != nil {
		t.Fatalf("ReportHostGatewayResult snapshot: %v", err)
	}
	select {
	case result := <-confirmed:
		if result.err != nil || result.operation.Status != OperationQueued {
			t.Fatalf("ConfirmAction = %#v, %v", result.operation, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("ConfirmAction did not resume after Host snapshot")
	}
}

func TestPollingActionHostFailsPendingRequestWhenHostLeaves(t *testing.T) {
	binder, engine := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	service, lease, _ := operationTestService(t, Options{PolicyEngine: engine})
	principal := acquireRemoteActionController(t, service)
	input := binder.input("request.remote.offline", "action.remote.offline")
	result := make(chan error, 1)
	go func() {
		_, err := service.SubmitAction(
			context.Background(),
			principal,
			input,
		)
		result <- err
	}()
	delivery := pollHost(t, service, lease, 1)
	if len(delivery.GatewayRequests) != 1 {
		t.Fatalf("PollHost = %#v", delivery)
	}
	if err := service.UnregisterHost("test.host", lease.LeaseID); err != nil {
		t.Fatalf("UnregisterHost: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("SubmitAction error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending binding was not failed when Host left")
	}
}

func TestPollingActionHostRejectsMalformedResultWithoutLosingRequest(t *testing.T) {
	binder, engine := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	service, lease, _ := operationTestService(t, Options{PolicyEngine: engine})
	principal := acquireRemoteActionController(t, service)
	result := make(chan error, 1)
	go func() {
		_, err := service.SubmitAction(
			context.Background(),
			principal,
			binder.input("request.remote.invalid", "action.remote.invalid"),
		)
		result <- err
	}()
	delivery := pollHost(t, service, lease, 1).GatewayRequests[0]
	if err := service.ReportHostGatewayResult(
		"test.host",
		lease.LeaseID,
		HostGatewayResult{GatewayRequestID: delivery.Request.GatewayRequestID},
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed result error = %v", err)
	}
	redelivery := pollHost(t, service, lease, 1)
	if len(redelivery.GatewayRequests) != 1 ||
		redelivery.GatewayRequests[0].Request.GatewayRequestID !=
			delivery.Request.GatewayRequestID {
		t.Fatalf("request lost after invalid result = %#v", redelivery)
	}
	binding, err := binder.BindAction(
		context.Background(),
		delivery.Request.Target,
		*delivery.Request.ActionRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostGatewayResult(
		"test.host",
		lease.LeaseID,
		HostGatewayResult{
			GatewayRequestID: delivery.Request.GatewayRequestID,
			Binding:          &binding,
		},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("SubmitAction: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SubmitAction did not complete")
	}
}

func acquireRemoteActionController(
	t *testing.T,
	service *Service,
) host.Principal {
	t.Helper()
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorControl,
		ScopeActorExecute,
		ScopeOperationCancel,
	)
	if _, err := service.AcquireController(
		principal,
		AcquireControllerInput{
			ActorControlTarget: testActorControlTarget(),
			ControllerID:       "controller.gateway.one",
			LeaseTTLMillis:     5_000,
		},
	); err != nil {
		t.Fatalf("AcquireController: %v", err)
	}
	return principal
}

func submitRemoteBoundAction(
	t *testing.T,
	service *Service,
	lease HostLease,
	principal host.Principal,
	binder *actionGatewayHost,
	requestID, idempotencyKey string,
) OperationView {
	t.Helper()
	type result struct {
		operation OperationView
		err       error
	}
	done := make(chan result, 1)
	go func() {
		operation, err := service.SubmitAction(
			context.Background(),
			principal,
			binder.input(requestID, idempotencyKey),
		)
		done <- result{operation: operation, err: err}
	}()
	delivery := pollHost(t, service, lease, 1).GatewayRequests[0]
	binding, err := binder.BindAction(
		context.Background(),
		delivery.Request.Target,
		*delivery.Request.ActionRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostGatewayResult(
		"test.host",
		lease.LeaseID,
		HostGatewayResult{
			GatewayRequestID: delivery.Request.GatewayRequestID,
			Binding:          &binding,
		},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-done:
		if value.err != nil {
			t.Fatalf("SubmitAction: %v", value.err)
		}
		return value.operation
	case <-time.After(time.Second):
		t.Fatal("SubmitAction did not complete")
		return OperationView{}
	}
}

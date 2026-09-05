package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

type lateActionBindingHost struct {
	*actionGatewayHost
	started chan struct{}
	release chan struct{}
}

func (gateway *lateActionBindingHost) BindAction(_ context.Context, target ActorControlTarget, request host.ActionRequest) (ActionBindingResult, error) {
	result, err := gateway.actionGatewayHost.BindAction(context.Background(), target, request)
	close(gateway.started)
	<-gateway.release
	return result, err
}

func TestCancelledSubmissionRejectsLateHostBinding(t *testing.T) {
	service, lease, principal, actionHost := actionGatewayTestService(t, host.RiskLow, policy.ProfileOpen)
	late := &lateActionBindingHost{actionGatewayHost: actionHost, started: make(chan struct{}), release: make(chan struct{})}
	service.actionHost = late
	input := actionHost.input("request.cancel-late", "action.cancel-late")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.SubmitAction(ctx, principal, input); done <- err }()
	select {
	case <-late.started:
	case <-time.After(time.Second):
		t.Fatal("binding did not start")
	}
	_, err := service.FindActionOperation(principal, input)
	if !errors.Is(err, ErrUnavailable) {
		close(late.release)
		t.Fatalf("in-flight lookup = %v", err)
	}
	cancel()
	close(late.release)
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("submission did not finish")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("late binding error = %v", err)
	}
	if batch := service.collectHostWorkForTest(t, lease); len(batch.Requests) != 0 {
		t.Fatal("late binding reached Host execution queue")
	}
	if _, err := service.FindActionOperation(principal, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled intent lookup = %v", err)
	}
}

func TestActionLookupRequiresExactIntentAndNeverSubmits(t *testing.T) {
	service, _, principal, actionHost := actionGatewayTestService(t, host.RiskLow, policy.ProfileOpen)
	input := actionHost.input("request.lookup", "action.lookup")
	if _, err := service.FindActionOperation(principal, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing lookup = %v", err)
	}
	if actionHost.bindCalls != 0 {
		t.Fatal("lookup bound an action")
	}
	submitted, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	found, err := service.FindActionOperation(principal, input)
	if err != nil || found.OperationID != submitted.OperationID {
		t.Fatalf("existing lookup = %#v %v", found, err)
	}
	input.Request.RequestID = "request.changed-intent"
	if _, err := service.FindActionOperation(principal, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed intent lookup = %v", err)
	}
	if actionHost.bindCalls != 1 {
		t.Fatal("lookup resubmitted an action")
	}
}

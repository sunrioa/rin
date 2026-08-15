package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestActionGatewayQueuesOnlyAllowedBoundAction(t *testing.T) {
	service, hostLease, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	operation, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.allowed", "action.allowed"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	if operation.Status != OperationQueued || operation.Terminal ||
		operation.ExecutionConfirmed || operation.ActionRequest == nil ||
		operation.BoundAction == nil || operation.PolicyDecision == nil ||
		operation.PolicyDecision.Result != policy.Allow ||
		operation.ControllerLeaseID == "" {
		t.Fatalf("queued action = %#v", operation)
	}

	batch := pollHost(t, service, hostLease, 1)
	if len(batch.Requests) != 1 {
		t.Fatalf("PollHost = %#v", batch)
	}
	delivery := batch.Requests[0].Request
	if delivery.Kind != ControlAction || delivery.ActionRequest == nil ||
		delivery.BoundAction == nil || delivery.PolicyDecision == nil ||
		delivery.PolicyDecision.Result != policy.Allow {
		t.Fatalf("action delivery = %#v", delivery)
	}
	if err := ValidateActionDelivery(delivery); err != nil {
		t.Fatalf("ValidateActionDelivery: %v", err)
	}
	tampered := cloneControlRequest(delivery)
	tampered.PolicyDecision.EffectDigest =
		"0000000000000000000000000000000000000000000000000000000000000000"
	if err := ValidateActionDelivery(tampered); err == nil {
		t.Fatal("ValidateActionDelivery accepted a mismatched policy decision")
	}
	if err := actionHost.registry.AuthorizeBoundAction(
		*delivery.BoundAction,
		actionHost.snapshot.Now,
		actionHost.snapshot.Epoch,
		actionHost.snapshot.ObservationSeq,
		delivery.Principal,
	); err != nil {
		t.Fatalf("Host final authorization: %v", err)
	}
	if err := service.AcknowledgeHost(
		"test.host",
		hostLease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	accepted, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || accepted.ExecutionConfirmed || accepted.Terminal {
		t.Fatalf("accepted action = %#v, %v", accepted, err)
	}
	if err := service.ReportHostRun(
		"test.host",
		hostLease.LeaseID,
		host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionRunning,
			ProgressSeq: 1,
			Progress:    50,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 11},
		},
	); err != nil {
		t.Fatalf("ReportHostRun: %v", err)
	}
	running, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || running.ExecutionConfirmed || running.Terminal {
		t.Fatalf("running action = %#v, %v", running, err)
	}
	if err := service.ReportHostOutcome(
		"test.host",
		hostLease.LeaseID,
		host.ActionOutcome{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			Summary:     "The Host applied the bound action.",
			Epoch:       testEpoch(),
			WorldSeq:    2,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 12},
		},
	); err != nil {
		t.Fatalf("ReportHostOutcome: %v", err)
	}
	succeeded, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || !succeeded.ExecutionConfirmed || !succeeded.Terminal ||
		succeeded.Status != OperationSucceeded || succeeded.Outcome == nil {
		t.Fatalf("succeeded action = %#v, %v", succeeded, err)
	}
}

func TestActionGatewayAdmitsRecentObservationWindow(t *testing.T) {
	t.Run("within gap", func(t *testing.T) {
		service, hostLease, principal, actionHost := actionGatewayTestService(
			t, host.RiskLow, policy.ProfileOpen)
		publication := v2WorldPublication(actionHost.spec)
		publication.Sequence = 2
		publication.Actors[0].ObservationSeq = 2
		publication.Actors[0].Observation.Sequence = 2
		if err := service.PublishWorld(
			"test.host", hostLease.LeaseID, publication,
		); err != nil {
			t.Fatalf("PublishWorld: %v", err)
		}
		actionHost.mu.Lock()
		actionHost.snapshot.ObservationSeq = 2
		actionHost.bindStarted = make(chan struct{})
		actionHost.releaseBind = make(chan struct{})
		actionHost.mu.Unlock()
		done := make(chan error, 1)
		go func() {
			_, err := service.SubmitAction(
				context.Background(), principal,
				actionHost.input("request.gap.accepted", "gap.accepted"))
			done <- err
		}()
		<-actionHost.bindStarted
		for seq := uint64(3); seq <= 4; seq++ {
			publication := v2WorldPublication(actionHost.spec)
			publication.Sequence = seq
			publication.Actors[0].ObservationSeq = seq
			publication.Actors[0].Observation.Sequence = seq
			if err := service.PublishWorld(
				"test.host", hostLease.LeaseID, publication,
			); err != nil {
				t.Fatalf("PublishWorld %d: %v", seq, err)
			}
		}
		close(actionHost.releaseBind)
		if err := <-done; err != nil {
			t.Fatalf("recent observation was rejected: %v", err)
		}
	})
	t.Run("beyond gap", func(t *testing.T) {
		service, hostLease, principal, actionHost := actionGatewayTestService(
			t, host.RiskLow, policy.ProfileOpen)
		publication := v2WorldPublication(actionHost.spec)
		publication.Sequence = 2
		publication.Actors[0].ObservationSeq = 2
		publication.Actors[0].Observation.Sequence = 2
		if err := service.PublishWorld(
			"test.host", hostLease.LeaseID, publication,
		); err != nil {
			t.Fatalf("PublishWorld: %v", err)
		}
		actionHost.mu.Lock()
		actionHost.snapshot.ObservationSeq = 2
		actionHost.bindStarted = make(chan struct{})
		actionHost.releaseBind = make(chan struct{})
		actionHost.mu.Unlock()
		done := make(chan error, 1)
		go func() {
			_, err := service.SubmitAction(
				context.Background(), principal,
				actionHost.input("request.gap.rejected", "gap.rejected"))
			done <- err
		}()
		<-actionHost.bindStarted
		stale := v2WorldPublication(actionHost.spec)
		stale.Sequence = 11
		stale.Actors[0].ObservationSeq = 11
		stale.Actors[0].Observation.Sequence = 11
		if err := service.PublishWorld(
			"test.host", hostLease.LeaseID, stale,
		); err != nil {
			t.Fatalf("PublishWorld 11: %v", err)
		}
		close(actionHost.releaseBind)
		if err := <-done; err == nil {
			t.Fatal("stale observation was accepted")
		}
	})
}

func TestActionGatewayConfirmationBindsExactEffect(t *testing.T) {
	service, hostLease, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskCritical,
		policy.ProfileOpen,
	)
	pending, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.confirm", "action.confirm"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	if pending.Status != OperationAwaitingConfirmation || pending.Terminal ||
		pending.PolicyDecision == nil ||
		pending.PolicyDecision.Result != policy.RequireConfirmation ||
		pending.PolicyDecision.Confirmation == nil ||
		pending.DeliveryAttempts != 0 {
		t.Fatalf("pending confirmation = %#v", pending)
	}
	approver := operationPrincipal(
		ScopeActorControl,
		"rin.policy.confirm",
	)
	confirmed, err := service.ConfirmAction(
		context.Background(),
		approver,
		pending.OperationID,
	)
	if err != nil || confirmed.Status != OperationQueued ||
		confirmed.PolicyDecision == nil ||
		confirmed.PolicyDecision.Result != policy.Allow ||
		confirmed.PolicyDecision.EffectDigest != pending.BoundAction.EffectDigest {
		t.Fatalf("ConfirmAction = %#v, %v", confirmed, err)
	}
	batch := pollHost(t, service, hostLease, 1)
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != pending.OperationID {
		t.Fatalf("confirmed delivery = %#v", batch)
	}
}

func TestActionGatewayCancellationDiscardsPendingConfirmation(t *testing.T) {
	service, _, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskCritical,
		policy.ProfileOpen,
	)
	pending, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.cancel-confirm", "action.cancel-confirm"),
	)
	if err != nil || pending.PolicyDecision == nil ||
		pending.PolicyDecision.Confirmation == nil {
		t.Fatalf("SubmitAction = %#v, %v", pending, err)
	}
	challenge := *pending.PolicyDecision.Confirmation
	if _, err := service.CancelOperation(principal, pending.OperationID); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	if _, err := service.policyEngine.Approve(
		challenge.ChallengeID,
		operationPrincipal("rin.policy.confirm"),
		actionHost.snapshot.Now,
	); err == nil {
		t.Fatal("cancelled operation left its confirmation challenge active")
	}
}

func TestActionGatewayEmergencyStopCreatesPolicyRejection(t *testing.T) {
	service, hostLease, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	if _, err := service.SetActorEmergencyStop(
		principal,
		testActorControlTarget(),
		true,
	); err != nil {
		t.Fatalf("SetActorEmergencyStop: %v", err)
	}
	rejected, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.stopped", "action.stopped"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	if rejected.Status != OperationRejected || !rejected.Terminal ||
		rejected.ExecutionConfirmed || rejected.PolicyDecision == nil ||
		rejected.PolicyDecision.Result != policy.Deny ||
		rejected.RejectionCode != "policy.emergency_stop" {
		t.Fatalf("stopped action = %#v", rejected)
	}
	if batch := service.collectHostWorkForTest(t, hostLease); len(batch.Requests) != 0 {
		t.Fatalf("policy rejection reached Host = %#v", batch)
	}
}

func TestActionGatewayCoalescesConcurrentIdempotentSubmission(t *testing.T) {
	service, _, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	actionHost.bindStarted = make(chan struct{})
	actionHost.releaseBind = make(chan struct{})
	input := actionHost.input("request.action.concurrent", "action.concurrent")
	type result struct {
		operation OperationView
		err       error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			operation, err := service.SubmitAction(
				context.Background(),
				principal,
				input,
			)
			results <- result{operation: operation, err: err}
		}()
	}
	<-actionHost.bindStarted
	close(actionHost.releaseBind)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil ||
		first.operation.OperationID != second.operation.OperationID {
		t.Fatalf("concurrent submissions = %#v, %#v", first, second)
	}
	actionHost.mu.Lock()
	bindCalls := actionHost.bindCalls
	actionHost.mu.Unlock()
	if bindCalls != 1 {
		t.Fatalf("BindAction calls = %d", bindCalls)
	}
}

func TestActionGatewayTracksParentAndChildOperations(t *testing.T) {
	service, hostLease, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	macro := registerActionGatewayMacro(t, actionHost, true)
	publishActionGatewaySpecs(t, service, hostLease, macro, actionHost.spec)
	parentInput := actionHost.input("request.action.parent", "action.parent")
	parentInput.Request.Capability = macro.Capability
	parentInput.Request.SpecDigest = macro.Digest
	parentInput.Request.TaskID = "task.action.parent"
	parent, err := service.SubmitAction(
		context.Background(),
		principal,
		parentInput,
	)
	if err != nil {
		t.Fatalf("parent SubmitAction: %v", err)
	}
	acceptActionOperation(t, service, hostLease, parent.OperationID)
	childInput := actionHost.input("request.action.child", "action.child")
	childInput.Request.TaskID = parentInput.Request.TaskID
	childInput.ParentOperationID = parent.OperationID
	child, err := service.SubmitAction(
		context.Background(),
		principal,
		childInput,
	)
	if err != nil || child.ParentOperationID != parent.OperationID {
		t.Fatalf("child SubmitAction = %#v, %v", child, err)
	}
	parent, err = service.GetOperation(principal, parent.OperationID)
	if err != nil || len(parent.ChildOperationIDs) != 1 ||
		parent.ChildOperationIDs[0] != child.OperationID {
		t.Fatalf("parent children = %#v, %v", parent, err)
	}
}

func TestActionGatewayRejectsInvalidActionParent(t *testing.T) {
	t.Run("atomic parent", func(t *testing.T) {
		service, hostLease, principal, actionHost := actionGatewayTestService(
			t, host.RiskLow, policy.ProfileOpen,
		)
		publishActionGatewaySpecs(t, service, hostLease, actionHost.spec)
		parentInput := actionHost.input("request.atomic.parent", "atomic.parent")
		parentInput.Request.TaskID = "task.atomic.parent"
		parent, err := service.SubmitAction(context.Background(), principal, parentInput)
		if err != nil {
			t.Fatalf("parent SubmitAction: %v", err)
		}
		acceptActionOperation(t, service, hostLease, parent.OperationID)
		childInput := actionHost.input("request.atomic.child", "atomic.child")
		childInput.Request.TaskID = parentInput.Request.TaskID
		childInput.ParentOperationID = parent.OperationID
		if _, err := service.SubmitAction(
			context.Background(), principal, childInput,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("atomic parent error = %v", err)
		}
	})

	t.Run("macro does not produce children", func(t *testing.T) {
		service, hostLease, principal, actionHost := actionGatewayTestService(
			t, host.RiskLow, policy.ProfileOpen,
		)
		macro := registerActionGatewayMacro(t, actionHost, false)
		publishActionGatewaySpecs(t, service, hostLease, macro, actionHost.spec)
		parentInput := actionHost.input("request.closed.parent", "closed.parent")
		parentInput.Request.Capability = macro.Capability
		parentInput.Request.SpecDigest = macro.Digest
		parentInput.Request.TaskID = "task.closed.parent"
		parent, err := service.SubmitAction(context.Background(), principal, parentInput)
		if err != nil {
			t.Fatalf("parent SubmitAction: %v", err)
		}
		acceptActionOperation(t, service, hostLease, parent.OperationID)
		childInput := actionHost.input("request.closed.child", "closed.child")
		childInput.Request.TaskID = parentInput.Request.TaskID
		childInput.ParentOperationID = parent.OperationID
		if _, err := service.SubmitAction(
			context.Background(), principal, childInput,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("closed macro parent error = %v", err)
		}
	})

	t.Run("macro is not accepted", func(t *testing.T) {
		service, hostLease, principal, actionHost := actionGatewayTestService(
			t, host.RiskLow, policy.ProfileOpen,
		)
		macro := registerActionGatewayMacro(t, actionHost, true)
		publishActionGatewaySpecs(t, service, hostLease, macro, actionHost.spec)
		parentInput := actionHost.input("request.queued.parent", "queued.parent")
		parentInput.Request.Capability = macro.Capability
		parentInput.Request.SpecDigest = macro.Digest
		parentInput.Request.TaskID = "task.queued.parent"
		parent, err := service.SubmitAction(context.Background(), principal, parentInput)
		if err != nil {
			t.Fatalf("parent SubmitAction: %v", err)
		}
		childInput := actionHost.input("request.queued.child", "queued.child")
		childInput.Request.TaskID = parentInput.Request.TaskID
		childInput.ParentOperationID = parent.OperationID
		if _, err := service.SubmitAction(
			context.Background(), principal, childInput,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("queued macro parent error = %v", err)
		}
	})

	t.Run("task or catalog changed", func(t *testing.T) {
		service, hostLease, principal, actionHost := actionGatewayTestService(
			t, host.RiskLow, policy.ProfileOpen,
		)
		macro := registerActionGatewayMacro(t, actionHost, true)
		publishActionGatewaySpecs(t, service, hostLease, macro, actionHost.spec)
		parentInput := actionHost.input("request.task.parent", "task.parent")
		parentInput.Request.Capability = macro.Capability
		parentInput.Request.SpecDigest = macro.Digest
		parentInput.Request.TaskID = "task.parent"
		parent, err := service.SubmitAction(context.Background(), principal, parentInput)
		if err != nil {
			t.Fatalf("parent SubmitAction: %v", err)
		}
		acceptActionOperation(t, service, hostLease, parent.OperationID)
		childInput := actionHost.input("request.task.child", "task.child")
		childInput.Request.TaskID = "task.other"
		childInput.ParentOperationID = parent.OperationID
		if _, err := service.SubmitAction(
			context.Background(), principal, childInput,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("mismatched task error = %v", err)
		}

		publication := v2WorldPublication(actionHost.spec)
		publication.Sequence = 3
		if err := service.PublishWorld(
			"test.host", hostLease.LeaseID, publication,
		); err != nil {
			t.Fatalf("PublishWorld without macro: %v", err)
		}
		childInput.Request.RequestID = "request.catalog.child"
		childInput.Request.IdempotencyKey = "catalog.child"
		childInput.Request.TaskID = parentInput.Request.TaskID
		if _, err := service.SubmitAction(
			context.Background(), principal, childInput,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing parent catalog error = %v", err)
		}
	})
}

func TestActionGatewayCancellationRollsBackPolicyBudget(t *testing.T) {
	service, _, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	if err := service.policyEngine.Update(policy.Config{
		Revision:           2,
		Profile:            policy.ProfileOpen,
		KnownEffectKinds:   []string{"world.position"},
		KnownScopes:        []string{"world.public"},
		ConfirmationTTL:    policy.ConfirmationDurations{Step: 20},
		ConfirmationScopes: []string{"rin.policy.confirm"},
		Budgets: []policy.Budget{{
			BudgetID:    "actor.action-limit",
			Layer:       policy.LayerActor,
			EffectKinds: []string{"world.position"},
			MaxActions:  1,
		}},
	}); err != nil {
		t.Fatalf("Policy Update: %v", err)
	}
	first, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.budget.first", "action.budget.first"),
	)
	if err != nil || first.Status != OperationQueued {
		t.Fatalf("first budget action = %#v, %v", first, err)
	}
	if _, err := service.CancelOperation(principal, first.OperationID); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	second, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.budget.second", "action.budget.second"),
	)
	if err != nil || second.Status != OperationQueued {
		t.Fatalf("second budget action = %#v, %v", second, err)
	}
	third, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.budget.third", "action.budget.third"),
	)
	if err != nil || third.Status != OperationRejected ||
		third.RejectionCode != "policy.budget_exceeded" {
		t.Fatalf("third budget action = %#v, %v", third, err)
	}
}

func TestActionGatewayCancellationWinsConfirmationRace(t *testing.T) {
	service, hostLease, principal, actionHost := actionGatewayTestService(
		t,
		host.RiskCritical,
		policy.ProfileOpen,
	)
	pending, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.confirm-race", "action.confirm-race"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	snapshotStarted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	actionHost.snapshotStarted = snapshotStarted
	actionHost.releaseSnapshot = releaseSnapshot
	confirmed := make(chan OperationView, 1)
	confirmErrors := make(chan error, 1)
	go func() {
		view, confirmErr := service.ConfirmAction(
			context.Background(),
			operationPrincipal(ScopeActorControl, "rin.policy.confirm"),
			pending.OperationID,
		)
		if confirmErr != nil {
			confirmErrors <- confirmErr
			return
		}
		confirmed <- view
	}()
	<-snapshotStarted
	cancelled, err := service.CancelOperation(principal, pending.OperationID)
	if err != nil || cancelled.Status != OperationCancelled {
		t.Fatalf("CancelOperation = %#v, %v", cancelled, err)
	}
	close(releaseSnapshot)
	select {
	case confirmErr := <-confirmErrors:
		t.Fatalf("ConfirmAction: %v", confirmErr)
	case view := <-confirmed:
		if view.Status != OperationCancelled || view.ExecutionConfirmed {
			t.Fatalf("confirmation race result = %#v", view)
		}
	}
	if batch := service.collectHostWorkForTest(t, hostLease); len(batch.Requests) != 0 {
		t.Fatalf("cancelled confirmation reached Host = %#v", batch)
	}
}

func TestActionGatewayRestartDoesNotReplayUnacceptedBinding(t *testing.T) {
	root := t.TempDir()
	now := testControlTime()
	actionHost, engine := actionGatewayTestComponents(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	configureActionGatewayBudget(t, engine, 1)
	options := fileTestOptions(&now, 0)
	options.ActionHost = actionHost
	options.PolicyEngine = engine
	service, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	registerAndPublishOperationHost(t, service, "instance.action.persisted")
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
	queued, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.persisted", "action.persisted"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, recoveredEngine := actionGatewayTestComponents(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	configureActionGatewayBudget(t, recoveredEngine, 1)
	options = fileTestOptions(&now, 128)
	options.ActionHost = actionHost
	options.PolicyEngine = recoveredEngine
	recovered, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile recovered: %v", err)
	}
	defer recovered.Close()
	view, err := recovered.GetOperation(principal, queued.OperationID)
	if err != nil || view.Status != OperationStale || !view.Terminal ||
		view.ExecutionConfirmed || view.BoundAction == nil ||
		view.PolicyDecision == nil {
		t.Fatalf("recovered V2 action = %#v, %v", view, err)
	}
	registerAndPublishOperationHost(t, recovered, "instance.action.recovered")
	second, err := recovered.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.after-restart", "action.after-restart"),
	)
	if err != nil || second.Status != OperationQueued {
		t.Fatalf("action after rolled-back recovery = %#v, %v", second, err)
	}
	third, err := recovered.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.budget-blocked", "action.budget-blocked"),
	)
	if err != nil || third.Status != OperationRejected ||
		third.RejectionCode != "policy.budget_exceeded" {
		t.Fatalf("budget after recovery = %#v, %v", third, err)
	}
}

func TestActionGatewayRejectsOrphanedPolicyReservationOnRestore(t *testing.T) {
	root := t.TempDir()
	now := testControlTime()
	actionHost, engine := actionGatewayTestComponents(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	configureActionGatewayBudget(t, engine, 1)
	options := fileTestOptions(&now, 0)
	options.ActionHost = actionHost
	options.PolicyEngine = engine
	service, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	registerAndPublishOperationHost(t, service, "instance.action.orphan")
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorControl,
		ScopeActorExecute,
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
	if _, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.action.orphan", "action.orphan"),
	); err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := filepath.Join(root, operationFileName)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var state persistedOperations
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if state.PolicyState == nil || len(state.PolicyState.Reservations) != 1 {
		t.Fatalf("policy state = %#v", state.PolicyState)
	}
	state.PolicyState.Reservations[0].DecisionID = "decision.orphaned"
	tampered, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, recoveredEngine := actionGatewayTestComponents(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	configureActionGatewayBudget(t, recoveredEngine, 1)
	options = fileTestOptions(&now, 128)
	options.ActionHost = actionHost
	options.PolicyEngine = recoveredEngine
	if _, err := OpenFile(root, options); !errors.Is(err, ErrPersistence) {
		t.Fatalf("OpenFile orphaned policy state error = %v", err)
	}

	state.PolicyState.Reservations = nil
	tampered, err = json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal missing reservation: %v", err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("WriteFile missing reservation: %v", err)
	}
	_, missingEngine := actionGatewayTestComponents(
		t,
		host.RiskLow,
		policy.ProfileOpen,
	)
	configureActionGatewayBudget(t, missingEngine, 1)
	options = fileTestOptions(&now, 256)
	options.ActionHost = actionHost
	options.PolicyEngine = missingEngine
	if _, err := OpenFile(root, options); !errors.Is(err, ErrPersistence) {
		t.Fatalf("OpenFile missing policy reservation error = %v", err)
	}
}

type actionGatewayHost struct {
	mu              sync.Mutex
	registry        *host.Registry
	spec            host.CapabilitySpec
	snapshot        ActionHostSnapshot
	risk            host.RiskLevel
	bindCalls       int
	bindStarted     chan struct{}
	releaseBind     chan struct{}
	snapshotStarted chan struct{}
	releaseSnapshot chan struct{}
}

func (gateway *actionGatewayHost) BindAction(
	ctx context.Context,
	target ActorControlTarget,
	request host.ActionRequest,
) (ActionBindingResult, error) {
	gateway.mu.Lock()
	gateway.bindCalls++
	call := gateway.bindCalls
	started := gateway.bindStarted
	release := gateway.releaseBind
	snapshot := gateway.snapshot
	if started != nil && call == 1 {
		close(started)
	}
	gateway.mu.Unlock()
	if release != nil {
		select {
		case <-ctx.Done():
			return ActionBindingResult{}, ctx.Err()
		case <-release:
		}
	}
	if target.HostID != "test.host" || target.WorldID != "world.one" ||
		target.ActorID != request.ActorID {
		return ActionBindingResult{}, errors.New("unexpected action target")
	}
	action, err := gateway.registry.SealBinding(
		request,
		host.BindingDraft{
			BindingID: fmt.Sprintf("binding.gateway.%d", call),
			Effects: []host.Effect{{
				EffectID:   fmt.Sprintf("effect.gateway.%d", call),
				Kind:       "world.position",
				Operation:  host.EffectOperationUpdate,
				Tags:       []string{"actor.movement"},
				Ownership:  host.OwnershipActor,
				Scope:      "world.public",
				Quantity:   1,
				Unit:       "step",
				Reversible: true,
				Risk:       gateway.risk,
				Attributes: json.RawMessage(`{}`),
			}},
			ValidUntil: host.Timepoint{
				Clock: snapshot.Now.Clock,
				Value: snapshot.Now.Value + 50,
			},
		},
		snapshot.Now,
		snapshot.Epoch,
		snapshot.ObservationSeq,
	)
	if err != nil {
		return ActionBindingResult{}, err
	}
	return ActionBindingResult{Action: action, Snapshot: snapshot}, nil
}

func (gateway *actionGatewayHost) SnapshotAction(
	ctx context.Context,
	target ActorControlTarget,
) (ActionHostSnapshot, error) {
	if target != testActorControlTarget() {
		return ActionHostSnapshot{}, errors.New("unexpected action target")
	}
	gateway.mu.Lock()
	started := gateway.snapshotStarted
	release := gateway.releaseSnapshot
	snapshot := gateway.snapshot
	if started != nil {
		gateway.snapshotStarted = nil
		close(started)
	}
	gateway.mu.Unlock()
	if release != nil {
		select {
		case <-ctx.Done():
			return ActionHostSnapshot{}, ctx.Err()
		case <-release:
		}
	}
	return snapshot, nil
}

func (gateway *actionGatewayHost) input(
	requestID, idempotencyKey string,
) SubmitActionInput {
	return SubmitActionInput{
		HostID:  "test.host",
		WorldID: "world.one",
		Request: host.ActionRequest{
			RequestID:      requestID,
			ControllerID:   "controller.gateway.one",
			ActorID:        "actor.one",
			Capability:     gateway.spec.Capability,
			SpecDigest:     gateway.spec.Digest,
			Arguments:      json.RawMessage(`{}`),
			ExpectedEpoch:  testEpoch(),
			ObservationSeq: 1,
			IdempotencyKey: idempotencyKey,
		},
	}
}

func registerActionGatewayMacro(
	t *testing.T,
	gateway *actionGatewayHost,
	producesChildren bool,
) host.CapabilitySpec {
	t.Helper()
	draft := gateway.spec
	draft.Capability = host.CapabilityRef{
		ID: "test.actor.macro", Version: "2.0.0",
	}
	draft.Description = "Run a bounded test macro through the Host adapter."
	draft.Kind = host.CapabilityMacro
	draft.Execution = host.ExecutionLongRunning
	draft.Cancellation = host.CancellationCooperative
	draft.ProducesChildOperations = producesChildren
	draft.Digest = ""
	sealed, err := gateway.registry.RegisterSpec(draft)
	if err != nil {
		t.Fatalf("RegisterSpec macro: %v", err)
	}
	return sealed
}

func publishActionGatewaySpecs(
	t *testing.T,
	service *Service,
	lease HostLease,
	specs ...host.CapabilitySpec,
) {
	t.Helper()
	publication := v2WorldPublication(specs[0])
	publication.Actors[0].Capabilities = &host.CapabilitySnapshot{
		Revision: 2,
		Specs:    append([]host.CapabilitySpec(nil), specs...),
	}
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, publication,
	); err != nil {
		t.Fatalf("PublishWorld V2 action catalog: %v", err)
	}
}

func acceptActionOperation(
	t *testing.T,
	service *Service,
	lease HostLease,
	operationID string,
) {
	t.Helper()
	batch := pollHost(t, service, lease, 1)
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != operationID {
		t.Fatalf("PollHost parent = %#v", batch)
	}
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost parent: %v", err)
	}
}

func actionGatewayTestService(
	t *testing.T,
	risk host.RiskLevel,
	profile policy.Profile,
) (*Service, HostLease, host.Principal, *actionGatewayHost) {
	t.Helper()
	actionHost, engine := actionGatewayTestComponents(t, risk, profile)
	service, hostLease, _ := operationTestService(t, Options{
		ActionHost:   actionHost,
		PolicyEngine: engine,
	})
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
	return service, hostLease, principal, actionHost
}

func actionGatewayTestComponents(
	t *testing.T,
	risk host.RiskLevel,
	profile policy.Profile,
) (*actionGatewayHost, *policy.Engine) {
	t.Helper()
	registry, err := host.NewRegistry(registration("instance.gateway.registry").Manifest)
	if err != nil {
		t.Fatal(err)
	}
	emptySchema, err := host.NewSchema([]byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := registry.RegisterSpec(host.CapabilitySpec{
		Capability:         host.CapabilityRef{ID: "test.actor.move", Version: "2.0.0"},
		Description:        "Move the test Actor through the Host adapter.",
		Input:              emptySchema,
		Output:             emptySchema,
		EffectSchema:       emptySchema,
		Kind:               host.CapabilityAtomic,
		Execution:          host.ExecutionImmediate,
		Cancellation:       host.CancellationUnsupported,
		RiskFloor:          host.RiskLow,
		RequiredDurability: host.DurabilityAdvisory,
		ExecutionBudget:    host.Duration{Clock: host.ClockStep, Value: 100},
		MaxInputBytes:      1_024,
		MaxOutputBytes:     1_024,
		MaxEffects:         4,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.New(policy.Config{
		Revision:           1,
		Profile:            profile,
		KnownEffectKinds:   []string{"world.position"},
		KnownScopes:        []string{"world.public"},
		ConfirmationTTL:    policy.ConfirmationDurations{Step: 20},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	actionHost := &actionGatewayHost{
		registry: registry,
		spec:     sealed,
		snapshot: ActionHostSnapshot{
			Now:            host.Timepoint{Clock: host.ClockStep, Value: 10},
			Epoch:          testEpoch(),
			ObservationSeq: 1,
		},
		risk: risk,
	}
	return actionHost, engine
}

func configureActionGatewayBudget(
	t *testing.T,
	engine *policy.Engine,
	maxActions uint32,
) {
	t.Helper()
	if err := engine.Update(policy.Config{
		Revision:           2,
		Profile:            policy.ProfileOpen,
		KnownEffectKinds:   []string{"world.position"},
		KnownScopes:        []string{"world.public"},
		ConfirmationTTL:    policy.ConfirmationDurations{Step: 20},
		ConfirmationScopes: []string{"rin.policy.confirm"},
		Budgets: []policy.Budget{{
			BudgetID:    "actor.action-limit",
			Layer:       policy.LayerActor,
			EffectKinds: []string{"world.position"},
			MaxActions:  maxActions,
		}},
	}); err != nil {
		t.Fatalf("Policy Update: %v", err)
	}
}

func testControlTime() time.Time {
	return time.UnixMilli(1_000_000)
}

func (service *Service) collectHostWorkForTest(
	t *testing.T,
	lease HostLease,
) HostControlBatch {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, err := service.requireLeaseLocked("test.host", lease.LeaseID); err != nil {
		t.Fatal(err)
	}
	return service.collectHostWorkLocked("test.host", 64)
}

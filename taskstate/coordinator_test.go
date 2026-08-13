package taskstate_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

func TestCoordinatorRequiresCurrentControllerAndLinksSubmittedAction(t *testing.T) {
	clock := time.UnixMilli(10)
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"),
		taskstate.StoreConfig{Now: func() time.Time { return clock }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testDraft("plan.one", "task.one")
	client := &planControlStub{
		principal: host.Principal{ID: "principal.one", GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl, controlplane.ScopeActorExecute,
		}},
		actor: controlplane.ActorView{
			HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
			Epoch: draft.BasedOnEpoch, ObservationSeq: draft.BasedOnObservationSequence,
		},
		lease: controlplane.ControllerLease{
			ControllerID: draft.ControllerID, PrincipalID: "principal.one",
			HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
			Source: controlplane.DecisionExternal, Epoch: draft.BasedOnEpoch,
		},
	}
	coordinator, err := taskstate.NewCoordinator(store, client)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := coordinator.CreatePlan(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	request := host.ActionRequest{
		ControllerID: plan.ControllerID, ActorID: plan.ActorID, TaskID: plan.TaskID,
		PlanStep: &host.PlanStepRef{
			PlanID: plan.PlanID, PlanRevision: plan.Revision, StepID: plan.CurrentStepID,
		},
	}
	operation, err := coordinator.SubmitStepAction(context.Background(), taskstate.SubmitStepActionInput{
		Action: controlplane.SubmitActionInput{
			HostID: plan.HostID, WorldID: plan.WorldID, Request: request,
		},
		ConditionIDs: []string{"condition.collected"},
	})
	if err != nil || operation.OperationID != "operation.stub" || client.submissions != 1 {
		t.Fatalf("operation = %#v, submissions=%d, err=%v", operation, client.submissions, err)
	}
	client.lease.PrincipalID = "principal.other"
	request.IdempotencyKey = "another"
	if _, err := coordinator.SubmitStepAction(context.Background(), taskstate.SubmitStepActionInput{
		Action: controlplane.SubmitActionInput{HostID: plan.HostID, WorldID: plan.WorldID, Request: request},
	}); !errors.Is(err, taskstate.ErrForbidden) {
		t.Fatalf("foreign controller error = %v", err)
	}
}

func TestOutcomeSinkReconcilesCrashWindowAndAdvancesPlan(t *testing.T) {
	clock := time.UnixMilli(10)
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"),
		taskstate.StoreConfig{Now: func() time.Time { return clock }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, err := store.Create(context.Background(), testDraft("plan.one", "task.one"))
	if err != nil {
		t.Fatal(err)
	}
	sink, err := taskstate.NewOutcomeSink(store)
	if err != nil {
		t.Fatal(err)
	}
	clock = time.UnixMilli(20)
	outcome := host.ActionOutcome{
		OperationID: "operation.crash-window", Status: host.ActionSucceeded,
		Summary: "The Host completed the unlinked step action.", Epoch: plan.BasedOnEpoch,
		WorldSeq: 5, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 20},
	}
	evidence := controlplane.OutcomeEvidence{
		TaskID: plan.TaskID, OperationID: outcome.OperationID, HostID: plan.HostID,
		WorldID: plan.WorldID, ActorID: plan.ActorID, ControllerID: plan.ControllerID,
		ExpectedEpoch: plan.BasedOnEpoch, ObservationSequence: 4,
		PlanStep: &host.PlanStepRef{
			PlanID: plan.PlanID, PlanRevision: plan.Revision, StepID: plan.CurrentStepID,
		},
		Outcome: outcome,
	}
	if err := sink.RecordOutcome(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	if err := sink.RecordOutcome(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	advanced, err := store.Get(context.Background(), plan.PlanID)
	if err != nil || advanced.CurrentStepID != "step.return" || advanced.Revision != plan.Revision+1 {
		t.Fatalf("reconciled plan = %#v, %v", advanced, err)
	}
}

type planControlStub struct {
	principal   host.Principal
	actor       controlplane.ActorView
	lease       controlplane.ControllerLease
	submissions int
}

func (client *planControlStub) Info(context.Context) (controlplane.ClientInfo, error) {
	return controlplane.ClientInfo{ContractVersion: controlplane.ContractVersion, Principal: client.principal}, nil
}

func (client *planControlStub) GetActor(
	context.Context, string, string, string,
) (controlplane.ActorView, error) {
	return client.actor, nil
}

func (client *planControlStub) GetController(
	context.Context, controlplane.ActorControlTarget,
) (controlplane.ControllerLease, error) {
	return client.lease, nil
}

func (client *planControlStub) GetObservation(
	context.Context, controlplane.ActorControlTarget,
) (host.ObservationEnvelope, error) {
	return host.ObservationEnvelope{}, errors.New("observation not configured")
}

func (client *planControlStub) GetOperation(
	context.Context, string,
) (controlplane.OperationView, error) {
	return controlplane.OperationView{}, errors.New("operation not configured")
}

func (client *planControlStub) SubmitAction(
	_ context.Context, _ controlplane.SubmitActionInput,
) (controlplane.OperationView, error) {
	client.submissions++
	return controlplane.OperationView{OperationID: "operation.stub"}, nil
}

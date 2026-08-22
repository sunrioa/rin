package taskstate_test

import (
	"context"
	"errors"
	"fmt"
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
		Capability: host.CapabilityRef{ID: "resource.harvest", Version: "1.0.0"},
		PlanStep: &host.PlanStepRef{
			PlanID: plan.PlanID, PlanRevision: plan.Revision, StepID: plan.CurrentStepID,
		},
	}
	unrelated := request
	unrelated.Capability = host.CapabilityRef{ID: "activity.wait", Version: "1.0.0"}
	if _, err := coordinator.SubmitStepAction(context.Background(), taskstate.SubmitStepActionInput{
		Action: controlplane.SubmitActionInput{
			HostID: plan.HostID, WorldID: plan.WorldID, Request: unrelated,
		},
		ConditionIDs: []string{"condition.collected"},
	}); !errors.Is(err, taskstate.ErrInvalid) || client.submissions != 0 {
		t.Fatalf("unrelated action condition error = %v, submissions=%d", err, client.submissions)
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

func TestCoordinatorCancelsRuntimeOwnedPlanAfterAuthorityChanges(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*planControlStub)
		publicError error
	}{
		{
			name: "actor unavailable",
			mutate: func(client *planControlStub) {
				client.actorErr = controlplane.ErrUnavailable
			},
			publicError: controlplane.ErrUnavailable,
		},
		{
			name: "epoch changed",
			mutate: func(client *planControlStub) {
				client.actor.Epoch.Timeline++
				client.lease.Epoch = client.actor.Epoch
			},
			publicError: taskstate.ErrConflict,
		},
		{
			name: "lease expired",
			mutate: func(client *planControlStub) {
				client.controllerErr = controlplane.ErrLeaseExpired
			},
			publicError: taskstate.ErrForbidden,
		},
		{
			name: "foreign controller",
			mutate: func(client *planControlStub) {
				client.lease.ControllerID = "controller.other"
			},
			publicError: taskstate.ErrForbidden,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := taskstate.OpenSQLiteStore(
				filepath.Join(t.TempDir(), "taskstate.db"),
				taskstate.StoreConfig{Now: func() time.Time { return time.UnixMilli(10) }},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			draft := testDraft("plan.runtime-owned", "task.runtime-owned")
			draft.ControllerSource = taskstate.ControllerInternal
			client := &planControlStub{
				principal: host.Principal{ID: "principal.internal", GrantedScopes: []string{
					controlplane.ScopeActorRead, controlplane.ScopeActorControl,
				}},
				actor: controlplane.ActorView{
					HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
					Epoch: draft.BasedOnEpoch, ObservationSeq: draft.BasedOnObservationSequence,
				},
				lease: controlplane.ControllerLease{
					ControllerID: draft.ControllerID, PrincipalID: "principal.internal",
					HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
					Source: controlplane.DecisionInternal, Epoch: draft.BasedOnEpoch,
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
			current, err := store.SetStatus(context.Background(), taskstate.StatusInput{
				PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
				Status: taskstate.PlanPaused, Summary: "Pause before the authority change.",
			})
			if err != nil {
				t.Fatal(err)
			}
			current, err = store.SetStatus(context.Background(), taskstate.StatusInput{
				PlanID: current.PlanID, ExpectedRevision: current.Revision,
				Status: taskstate.PlanActive, Summary: "Resume before the authority change.",
			})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(client)
			if _, err := coordinator.SetPlanStatus(context.Background(), taskstate.StatusInput{
				PlanID: current.PlanID, ExpectedRevision: current.Revision,
				Status: taskstate.PlanCancelled, Summary: "Public cancellation remains authorized.",
			}); !errors.Is(err, test.publicError) {
				t.Fatalf("public cancellation error = %v, want %v", err, test.publicError)
			}
			cancelled, err := coordinator.CancelOwnedPlan(
				context.Background(), ownedCancellation(plan),
			)
			if err != nil || cancelled.Status != taskstate.PlanCancelled ||
				cancelled.Revision != current.Revision+1 {
				t.Fatalf("owned cancellation = %#v, err=%v", cancelled, err)
			}
			again, err := coordinator.CancelOwnedPlan(
				context.Background(), ownedCancellation(plan),
			)
			if err != nil || again.Status != taskstate.PlanCancelled ||
				again.Revision != cancelled.Revision {
				t.Fatalf("idempotent owned cancellation = %#v, err=%v", again, err)
			}
		})
	}
}

func TestCoordinatorOwnedCancellationPreservesOwnershipAndOperationGuards(t *testing.T) {
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"),
		taskstate.StoreConfig{Now: func() time.Time { return time.UnixMilli(10) }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testDraft("plan.runtime-guard", "task.runtime-guard")
	draft.ControllerSource = taskstate.ControllerInternal
	client := &planControlStub{
		principal: host.Principal{ID: "principal.internal", GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl,
		}},
		actor: controlplane.ActorView{
			HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
			Epoch: draft.BasedOnEpoch, ObservationSeq: draft.BasedOnObservationSequence,
		},
		lease: controlplane.ControllerLease{
			ControllerID: draft.ControllerID, PrincipalID: "principal.internal",
			HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
			Source: controlplane.DecisionInternal, Epoch: draft.BasedOnEpoch,
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
	foreign := ownedCancellation(plan)
	foreign.TaskID = "task.other"
	if _, err := coordinator.CancelOwnedPlan(context.Background(), foreign); !errors.Is(err, taskstate.ErrForbidden) {
		t.Fatalf("foreign task cancellation error = %v", err)
	}
	if err := store.LinkOperation(context.Background(), taskstate.OperationLink{
		OperationID: "operation.running", PlanID: plan.PlanID,
		PlanRevision: plan.Revision, StepID: plan.CurrentStepID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CancelOwnedPlan(
		context.Background(), ownedCancellation(plan),
	); !errors.Is(err, taskstate.ErrConflict) {
		t.Fatalf("cancellation with unfinished operation error = %v", err)
	}
	current, err := store.Get(context.Background(), plan.PlanID)
	if err != nil || current.Status != taskstate.PlanActive || current.Revision != plan.Revision {
		t.Fatalf("guarded plan changed = %#v, err=%v", current, err)
	}
}

func ownedCancellation(plan taskstate.PlanState) taskstate.OwnedPlanCancellationInput {
	return taskstate.OwnedPlanCancellationInput{
		PlanID: plan.PlanID,
		TaskID: plan.TaskID, SessionID: plan.SessionID,
		HostID: plan.HostID, WorldID: plan.WorldID, ActorID: plan.ActorID,
		ControllerID: plan.ControllerID, Summary: "The owning runtime task terminated.",
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
		Capability: host.CapabilityRef{ID: "resource.harvest", Version: "1.0.0"},
		PlanStep: &host.PlanStepRef{
			PlanID: plan.PlanID, PlanRevision: plan.Revision, StepID: plan.CurrentStepID,
		},
		Outcome: outcome,
	}
	unrelatedOutcome := evidence
	unrelatedOutcome.OperationID = "operation.unrelated"
	unrelatedOutcome.Outcome.OperationID = unrelatedOutcome.OperationID
	unrelatedOutcome.Capability = host.CapabilityRef{ID: "activity.wait", Version: "1.0.0"}
	if err := sink.RecordOutcome(context.Background(), unrelatedOutcome); err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.Get(context.Background(), plan.PlanID)
	if err != nil || unchanged.CurrentStepID != plan.CurrentStepID || unchanged.Revision != plan.Revision {
		t.Fatalf("unrelated outcome advanced plan = %#v, %v", unchanged, err)
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

func TestCoordinatorReplansOnlyFromCurrentEpochOrCapabilityEvidence(t *testing.T) {
	clock := time.UnixMilli(10)
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"),
		taskstate.StoreConfig{Now: func() time.Time { return clock }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testDraft("plan.replan", "task.replan")
	draft.Steps[0].CapabilityHints = []host.CapabilityRef{{ID: "resource.harvest", Version: "1.0.0"}}
	client := &planControlStub{
		principal: host.Principal{ID: "principal.one", GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl,
		}},
		actor: controlplane.ActorView{
			HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
			Epoch: draft.BasedOnEpoch, ObservationSeq: draft.BasedOnObservationSequence, Online: true,
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
	revision := draft
	revision.Steps[0].CapabilityHints = []host.CapabilityRef{{ID: "inventory.pickup", Version: "1.0.0"}}
	revision.Steps[0].SuccessConditions[0].Capability =
		&host.CapabilityRef{ID: "inventory.pickup", Version: "1.0.0"}
	revised, err := coordinator.RevisePlan(context.Background(), taskstate.ReviseInput{
		PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
		Reason:  taskstate.ReplanRequiredCapabilityMissing,
		Summary: "The required capability is no longer published.", Draft: revision,
	})
	if err != nil {
		t.Fatalf("capability-backed replan failed: %v", err)
	}
	if _, err := store.SetStatus(context.Background(), taskstate.StatusInput{
		PlanID: revised.PlanID, ExpectedRevision: revised.Revision,
		Status: taskstate.PlanCancelled, Summary: "Finish the capability replan case.",
	}); err != nil {
		t.Fatal(err)
	}

	secondDraft := testDraft("plan.epoch", "task.epoch")
	client.actor.Epoch = secondDraft.BasedOnEpoch
	client.actor.ObservationSeq = secondDraft.BasedOnObservationSequence
	client.lease.Epoch = secondDraft.BasedOnEpoch
	second, err := coordinator.CreatePlan(context.Background(), secondDraft)
	if err != nil {
		t.Fatal(err)
	}
	newEpoch := second.BasedOnEpoch
	newEpoch.Timeline++
	client.actor.Epoch = newEpoch
	client.actor.ObservationSeq++
	client.lease.Epoch = newEpoch
	epochRevision := secondDraft
	epochRevision.BasedOnEpoch = newEpoch
	epochRevision.BasedOnObservationSequence = client.actor.ObservationSeq
	if _, err := coordinator.RevisePlan(context.Background(), taskstate.ReviseInput{
		PlanID: second.PlanID, ExpectedRevision: second.Revision,
		Reason:  taskstate.ReplanEpochInvalidated,
		Summary: "The world timeline changed.", Draft: epochRevision,
	}); err != nil {
		t.Fatalf("epoch-backed replan failed: %v", err)
	}
}

func TestCoordinatorAllowsReplanAtFailureThresholdBeforeAttemptLimit(t *testing.T) {
	clock := time.UnixMilli(10)
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"),
		taskstate.StoreConfig{Now: func() time.Time { return clock }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testDraft("plan.failures", "task.failures")
	draft.Steps[0].MaxAttempts = 5
	client := &planControlStub{
		principal: host.Principal{ID: "principal.one", GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl,
		}},
		actor: controlplane.ActorView{
			HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
			Epoch: draft.BasedOnEpoch, ObservationSeq: draft.BasedOnObservationSequence, Online: true,
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
	for attempt := 1; attempt <= 3; attempt++ {
		operationID := fmt.Sprintf("operation.failure.%d", attempt)
		if err := store.LinkOperation(context.Background(), taskstate.OperationLink{
			OperationID: operationID, PlanID: plan.PlanID, PlanRevision: plan.Revision,
			StepID: plan.CurrentStepID,
		}); err != nil {
			t.Fatal(err)
		}
		clock = time.UnixMilli(int64(10 + attempt))
		plan, _, err = store.ApplyOperationResult(context.Background(), taskstate.OperationResult{
			OperationID: operationID, ExecutionConfirmed: true,
			Outcome: host.ActionOutcome{
				OperationID: operationID, Status: host.ActionFailed, Code: "path.blocked",
				Summary: "The Host could not reach the target.", Epoch: draft.BasedOnEpoch,
				WorldSeq:   uint64(attempt + 1),
				OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: int64(attempt + 1)},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if plan.Status != taskstate.PlanActive || plan.ConsecutiveFailures != 3 {
		t.Fatalf("plan should remain active at global threshold: %#v", plan)
	}
	revision := draft
	revision.Phase = "Alternative route"
	if _, err := coordinator.RevisePlan(context.Background(), taskstate.ReviseInput{
		PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
		Reason:  taskstate.ReplanFailureThresholdReached,
		Summary: "Three authoritative failures require another route.", Draft: revision,
	}); err != nil {
		t.Fatalf("failure-backed replan failed before attempt limit: %v", err)
	}
}

type planControlStub struct {
	principal     host.Principal
	actor         controlplane.ActorView
	actorErr      error
	lease         controlplane.ControllerLease
	controllerErr error
	catalog       host.CapabilitySnapshot
	submissions   int
}

func (client *planControlStub) Info(context.Context) (controlplane.ClientInfo, error) {
	return controlplane.ClientInfo{ContractVersion: controlplane.ContractVersion, Principal: client.principal}, nil
}

func (client *planControlStub) GetActor(
	context.Context, string, string, string,
) (controlplane.ActorView, error) {
	return client.actor, client.actorErr
}

func (client *planControlStub) GetController(
	context.Context, controlplane.ActorControlTarget,
) (controlplane.ControllerLease, error) {
	return client.lease, client.controllerErr
}

func (client *planControlStub) GetObservation(
	context.Context, controlplane.ActorControlTarget,
) (host.ObservationEnvelope, error) {
	return host.ObservationEnvelope{}, errors.New("observation not configured")
}

func (client *planControlStub) ListCapabilities(
	context.Context, controlplane.ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	return client.catalog, nil
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

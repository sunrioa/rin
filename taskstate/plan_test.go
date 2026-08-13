package taskstate_test

import (
	"errors"
	"testing"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

func TestPlanReducerAdvancesOnlyWithMatchingCurrentEpochEvidence(t *testing.T) {
	plan := testPlan(t)
	first := plan.Steps[0].SuccessConditions[0]
	evidence := taskstate.PlanEvidence{
		EvidenceID: "evidence.one", ConditionID: first.ConditionID,
		Kind: taskstate.EvidenceOperationOutcome, OperationID: "operation.one",
		Epoch: plan.BasedOnEpoch, ObservationSequence: plan.BasedOnObservationSequence,
		Digest:               "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecordedAtUnixMillis: 11,
	}
	next, advanced, err := taskstate.ApplyEvidence(plan, evidence, 12)
	if err != nil || !advanced || next.CurrentStepID != "step.two" ||
		next.Steps[0].Status != taskstate.StepCompleted ||
		next.Steps[1].Status != taskstate.StepActive {
		t.Fatalf("advanced plan = %#v, %v, %v", next, advanced, err)
	}
	replayed, changed, err := taskstate.ApplyEvidence(next, evidence, 13)
	if err != nil || changed || replayed.Revision != next.Revision {
		t.Fatalf("evidence replay = %#v, %v, %v", replayed, changed, err)
	}
	stale := evidence
	stale.EvidenceID = "evidence.stale"
	stale.ConditionID = next.Steps[1].SuccessConditions[0].ConditionID
	stale.Epoch.Timeline++
	if _, _, err := taskstate.ApplyEvidence(next, stale, 14); !errors.Is(err, taskstate.ErrInvalid) {
		t.Fatalf("stale evidence error = %v", err)
	}
}

func TestPlanReducerBlocksAfterBoundedFailures(t *testing.T) {
	plan := testPlan(t)
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		plan, err = taskstate.ApplyFailure(
			plan, "operation.failure", "navigation", "path.blocked", int64(10+attempt),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if plan.Status != taskstate.PlanBlocked || plan.Steps[0].Status != taskstate.StepBlocked ||
		plan.ConsecutiveFailures != 3 {
		t.Fatalf("blocked plan = %#v", plan)
	}
}

func TestReplanPolicyIsDeterministicAndBounded(t *testing.T) {
	policy := taskstate.ReplanPolicy{FailureThreshold: 3, MaxReplans: 2}
	if taskstate.ShouldReplan(policy, taskstate.ReplanInput{
		Reason: taskstate.ReplanFailureThresholdReached, ConsecutiveFailures: 2,
		HasAuthoritativeProof: true,
	}) {
		t.Fatal("replanned below the failure threshold")
	}
	if !taskstate.ShouldReplan(policy, taskstate.ReplanInput{
		Reason: taskstate.ReplanFailureThresholdReached, ConsecutiveFailures: 3,
		HasAuthoritativeProof: true,
	}) {
		t.Fatal("did not replan at the authoritative failure threshold")
	}
	if taskstate.ShouldReplan(policy, taskstate.ReplanInput{
		Reason: taskstate.ReplanManualAuthorized, PlayerAuthorized: false,
	}) {
		t.Fatal("accepted an unauthorised manual replan")
	}
	if taskstate.ShouldReplan(policy, taskstate.ReplanInput{
		Reason: taskstate.ReplanEpochInvalidated, HasAuthoritativeProof: true, ReplanCount: 2,
	}) {
		t.Fatal("exceeded the replan budget")
	}
}

func testPlan(t *testing.T) taskstate.PlanState {
	t.Helper()
	epoch := host.Epoch{
		SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
	}
	plan, err := taskstate.NewPlan(taskstate.Draft{
		PlanID: "plan.one", TaskID: "task.one", SessionID: "session.one",
		HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		ControllerID: "controller.one", ControllerSource: taskstate.ControllerExternal,
		Goal: "Collect enough material and return home.", PlanningMode: taskstate.PlanningAuto,
		Steps: []taskstate.StepDraft{
			{
				StepID: "step.one", Title: "Collect", Objective: "Collect the material.",
				MaxAttempts: 3, SuccessConditions: []taskstate.PlanCondition{{
					ConditionID: "condition.collected", Kind: taskstate.EvidenceOperationOutcome,
					Summary: "The Host confirms the collection.",
				}},
			},
			{
				StepID: "step.two", Title: "Return", Objective: "Return to the starting area.",
				MaxAttempts: 3, SuccessConditions: []taskstate.PlanCondition{{
					ConditionID: "condition.returned", Kind: taskstate.EvidenceOperationOutcome,
					Summary: "The Host confirms the return.",
				}},
			},
		},
		MaxReplans: 2, BasedOnEpoch: epoch, BasedOnObservationSequence: 4,
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

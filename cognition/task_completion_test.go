package cognition_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

func startCompletionTask(t *testing.T, runtime *cognition.AgentRuntime, policy cognition.TaskCompletionPolicy) cognition.TaskSession {
	t.Helper()
	task, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.completion", HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Meet the acceptance criteria.", PlanningMode: taskstate.PlanningDisabled, Completion: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func completionFact(id string) taskstate.PlanCondition {
	return taskstate.PlanCondition{ConditionID: id, Kind: taskstate.EvidenceObservationFact, Summary: "Host confirms the condition.", FactID: id, FactValueJSON: "true"}
}
func publishCompletionFacts(fixture *agentRuntimeFixture, ids ...string) {
	fixture.control.actor.ObservationSeq++
	fixture.environment.observation.Sequence++
	fixture.environment.observation.ObservationID = fmt.Sprintf("observation.%d", fixture.environment.observation.Sequence)
	fixture.environment.observation.Facts = nil
	for _, id := range ids {
		fixture.environment.observation.Facts = append(fixture.environment.observation.Facts, host.ObservationFact{FactID: id, Kind: "goal.state", Value: []byte("true")})
	}
}

func TestCompletionWaitsForCurrentHostEvidenceAcrossRestartWithoutPlan(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{Kind: cognition.ModelDecisionComplete, Summary: "Done."}}
	runtime := fixture.runtime(t, 16)
	task := startCompletionTask(t, runtime, cognition.TaskCompletionPolicy{Mode: cognition.CompletionEvidence, Conditions: []taskstate.PlanCondition{completionFact("goal.arrived")}})
	waiting, err := runtime.RunTask(context.Background(), task.TaskID)
	if err != nil || waiting.Status == cognition.TaskCompleted || !waiting.CompletionRequested || waiting.PlanID != "" || waiting.Schedule.Kind != cognition.ScheduleObservation {
		t.Fatalf("unsupported completion was accepted: %#v %v", waiting, err)
	}
	snapshot, err := fixture.tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.tasks, err = cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	publishCompletionFacts(fixture, "goal.arrived")
	restored := fixture.runtime(t, 16)
	completed, err := restored.RunTask(context.Background(), task.TaskID)
	if err != nil || completed.Status != cognition.TaskCompleted || completed.PlanID != "" || len(fixture.model.inputs) != 1 {
		t.Fatalf("Host evidence did not finish directly: %#v %v", completed, err)
	}
}

func TestCompletionFactsMustHoldInOneObservation(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{Kind: cognition.ModelDecisionComplete, Summary: "Done."}, {Kind: cognition.ModelDecisionComplete, Summary: "Done."}}
	runtime := fixture.runtime(t, 16)
	task := startCompletionTask(t, runtime, cognition.TaskCompletionPolicy{Mode: cognition.CompletionEvidence, Conditions: []taskstate.PlanCondition{completionFact("goal.one"), completionFact("goal.two")}})
	publishCompletionFacts(fixture, "goal.one")
	if _, err := runtime.RunTask(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	publishCompletionFacts(fixture, "goal.two")
	waiting, err := runtime.RunTask(context.Background(), task.TaskID)
	if err != nil || waiting.Status == cognition.TaskCompleted || len(waiting.CompletionEvidence) != 1 {
		t.Fatalf("stale facts accumulated into proof: %#v %v", waiting, err)
	}
	publishCompletionFacts(fixture, "goal.one", "goal.two")
	completed, err := runtime.RunTask(context.Background(), task.TaskID)
	if err != nil || completed.Status != cognition.TaskCompleted {
		t.Fatalf("simultaneous facts did not complete: %#v %v", completed, err)
	}
}

func TestCompletionRequiresMatchingConfirmedOperationOutcome(t *testing.T) {
	for _, matching := range []bool{true, false} {
		t.Run(fmt.Sprint(matching), func(t *testing.T) {
			fixture := newAgentRuntimeFixture(t)
			decision := agentActionDecision()
			fixture.model.decisions = []cognition.ModelDecision{decision, {Kind: cognition.ModelDecisionComplete, Summary: "Done."}}
			fixture.control.operationAfterSubmit = succeededAgentOperation(fixture.environment.observation)
			capability := decision.Capability
			if !matching {
				capability.ID = "other.capability"
			}
			runtime := fixture.runtime(t, 16)
			task := startCompletionTask(t, runtime, cognition.TaskCompletionPolicy{Mode: cognition.CompletionEvidence, Conditions: []taskstate.PlanCondition{{ConditionID: "goal.action", Kind: taskstate.EvidenceOperationOutcome, Summary: "The selected capability succeeded.", Capability: &capability}}})
			result, err := runtime.RunTask(context.Background(), task.TaskID)
			if err != nil || (result.Status == cognition.TaskCompleted) != matching {
				t.Fatalf("outcome matching=%v completed=%v err=%v", matching, result.Status, err)
			}
		})
	}
}

func TestHumanCompletionRequiresExactReviewAndCannotReviveCancellation(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			fixture := newAgentRuntimeFixture(t)
			fixture.model.decisions = []cognition.ModelDecision{{Kind: cognition.ModelDecisionComplete, Summary: "Ready for acceptance."}}
			runtime := fixture.runtime(t, 16)
			task := startCompletionTask(t, runtime, cognition.TaskCompletionPolicy{Mode: cognition.CompletionHuman})
			review, err := runtime.RunTask(context.Background(), task.TaskID)
			if err != nil || review.Status != cognition.TaskPaused || review.PauseCode != "completion.confirmation-required" {
				t.Fatalf("human review bypassed: %#v %v", review, err)
			}
			if _, err := runtime.ConfirmTaskCompletion(context.Background(), task.TaskID, review.Revision-1); !errors.Is(err, cognition.ErrTaskRevisionConflict) {
				t.Fatalf("stale confirmation = %v", err)
			}
			if cancelFirst {
				cancelled, err := runtime.CancelTask(context.Background(), task.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := runtime.ConfirmTaskCompletion(context.Background(), task.TaskID, cancelled.Revision); !errors.Is(err, cognition.ErrProviderConflict) {
					t.Fatalf("cancelled task accepted confirmation: %v", err)
				}
			} else {
				completed, err := runtime.ConfirmTaskCompletion(context.Background(), task.TaskID, review.Revision)
				if err != nil || completed.Status != cognition.TaskCompleted {
					t.Fatalf("valid confirmation = %#v %v", completed, err)
				}
			}
		})
	}
}

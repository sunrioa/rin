package cognition

import (
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
	"testing"
)

func TestCompletionOperationRequirementsCountDistinctExactHostResults(t *testing.T) {
	epoch := host.Epoch{SessionID: "session.test", WorldID: "world.test", Host: 1, World: 1, Timeline: 1}
	capability := host.CapabilityRef{ID: "item.collect", Version: "1.0.0"}
	target := host.HostRef{Namespace: "game.test", Type: "entity", Key: "tree.one", Epoch: epoch}
	policy := TaskCompletionPolicy{Mode: CompletionEvidence, Conditions: []taskstate.PlanCondition{{ConditionID: "goal.collect", Kind: taskstate.EvidenceOperationOutcome, Summary: "Collect twice from this tree.", Capability: &capability}}, OperationRequirements: []CompletionOperationRequirement{{ConditionID: "goal.collect", ArgumentsJSON: `{"count":1,"item":"wood"}`, TargetRefs: []host.HostRef{target}, MinimumCount: 2}}}
	if _, err := normalizeTaskCompletion(policy); err != nil {
		t.Fatal(err)
	}
	task := TaskSession{TaskID: "task.one", WorldID: epoch.WorldID, Completion: policy, ControllerLease: controlplane.ControllerLease{Epoch: epoch}}
	request := host.ActionRequest{TaskID: task.TaskID, Capability: capability, Arguments: []byte(`{"item":"wood","count":1}`), Targets: []host.HostRef{target}}
	view := controlplane.OperationView{OperationID: "operation.one", ActionRequest: &request, BoundAction: &host.BoundAction{ResolvedTargets: []host.HostRef{target}}, ExecutionConfirmed: true, Outcome: &host.ActionOutcome{Status: host.ActionSucceeded, Epoch: epoch, WorldSeq: 2}}
	task.PendingOperationID = view.OperationID
	request.Arguments = []byte(`{"item":"stone","count":1}`)
	recordCompletionOutcome(&task, view)
	if len(task.CompletionEvidence) != 0 {
		t.Fatal("wrong arguments counted")
	}
	request.Arguments = []byte(`{"item":"wood","count":1}`)
	view.BoundAction.ResolvedTargets = nil
	recordCompletionOutcome(&task, view)
	if len(task.CompletionEvidence) != 0 {
		t.Fatal("wrong Host target counted")
	}
	view.BoundAction.ResolvedTargets = []host.HostRef{target}
	recordCompletionOutcome(&task, view)
	recordCompletionOutcome(&task, view)
	if len(task.CompletionEvidence) != 1 || taskCompletionSatisfied(task, epoch) {
		t.Fatal("duplicate delivery counted twice")
	}
	view.OperationID = "operation.two"
	task.PendingOperationID = view.OperationID
	recordCompletionOutcome(&task, view)
	if !taskCompletionSatisfied(task, epoch) || validateTaskCompletionEvidence(task) != nil {
		t.Fatal("distinct exact results did not satisfy")
	}
	refreshed := epoch
	refreshed.Timeline++
	refreshCompletionFacts(&task, host.ObservationEnvelope{Epoch: refreshed, Sequence: 3})
	if len(task.CompletionEvidence) != 0 || taskCompletionSatisfied(task, refreshed) {
		t.Fatal("evidence crossed epoch")
	}
	policy.OperationRequirements[0].ArgumentsJSON = `{"count":1,"count":2}`
	if _, err := normalizeTaskCompletion(policy); err == nil {
		t.Fatal("ambiguous JSON accepted")
	}
}

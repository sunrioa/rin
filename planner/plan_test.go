package planner

import "testing"

func validPlan() Plan {
	return Plan{
		ID:       "plan.survival",
		Revision: 1,
		Goal:     "prepare survival basics",
		Budget:   Budget{MaxSteps: 32, MaxWorldMutations: 16, MaxTicks: 6000},
		Nodes: []Node{
			{ID: "collect", Kind: Action, Capability: "resource.harvest", MaxAttempts: 3, WorldMutations: 1, Risk: "high"},
			{ID: "check", Kind: Branch, DependsOn: []string{"collect"}, When: []string{"has_material"}, Then: []string{"build"}, Else: []string{"wait"}, MaxAttempts: 1, Risk: "low"},
			{ID: "build", Kind: Action, Capability: "inventory.place", DependsOn: []string{"check"}, Priority: 5, MaxAttempts: 3, WorldMutations: 1, Risk: "high"},
			{ID: "wait", Kind: Action, Capability: "activity.wait", DependsOn: []string{"check"}, Priority: 1, MaxAttempts: 1, Risk: "low"},
		},
	}
}

func TestValidateAndReady(t *testing.T) {
	plan := validPlan()
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	ready := plan.Ready(State{}, map[string]bool{})
	if len(ready) != 1 || ready[0].ID != "collect" {
		t.Fatalf("ready = %#v", ready)
	}
	ready = plan.Ready(State{Completed: map[string]bool{"collect": true}}, map[string]bool{"has_material": true})
	if len(ready) != 1 || ready[0].ID != "check" {
		t.Fatalf("branch ready = %#v", ready)
	}
}

func TestValidateRejectsCycleAndUnboundedLoop(t *testing.T) {
	cycle := validPlan()
	cycle.Nodes[0].DependsOn = []string{"build"}
	if err := cycle.Validate(); err == nil {
		t.Fatal("cycle was accepted")
	}
	loop := validPlan()
	loop.Nodes = append(loop.Nodes, Node{ID: "loop", Kind: Loop, Children: []string{"collect"}, MaxAttempts: 1, Risk: "moderate"})
	if err := loop.Validate(); err == nil {
		t.Fatal("loop without bound was accepted")
	}
	branchCycle := validPlan()
	branchCycle.Nodes[2].Then = []string{"check"}
	if err := branchCycle.Validate(); err == nil {
		t.Fatal("branch cycle was accepted")
	}
}

func TestApplyEnforcesBudgetsWithoutMutatingInput(t *testing.T) {
	plan := validPlan()
	state := State{}
	next, err := plan.Apply(state, "collect", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps != 0 || next.Steps != 1 || !next.Completed["collect"] {
		t.Fatalf("state was mutated or not advanced: before=%#v after=%#v", state, next)
	}
	if _, err := plan.Apply(next, "build", 11, 1); err == nil {
		t.Fatal("dependency was ignored")
	}
	budget := plan
	budget.Budget.MaxWorldMutations = 1
	budget.Nodes[1].WorldMutations = 1
	if _, err := budget.Apply(next, "check", 12, 1); err == nil {
		t.Fatal("world mutation budget was ignored")
	}
}

func TestReadyUsesPriorityAsDeterministicTieBreaker(t *testing.T) {
	plan := validPlan()
	state := State{Completed: map[string]bool{"collect": true, "check": true}}
	ready := plan.Ready(state, map[string]bool{})
	if len(ready) != 2 || ready[0].ID != "build" || ready[1].ID != "wait" {
		t.Fatalf("priority order = %#v", ready)
	}
}

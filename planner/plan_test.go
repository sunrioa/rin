package planner

import (
	"fmt"
	"testing"
)

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
			{ID: "finish", Kind: Action, Capability: "activity.wait", DependsOn: []string{"build", "wait"}, MaxAttempts: 1, Risk: "low"},
		},
	}
}

func TestAdvanceSelectsOnlyOneBranchPath(t *testing.T) {
	plan := validPlan()
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	state := State{}
	assertReady(t, plan, state, "collect")
	state = mustApply(t, plan, state, "collect", 100, 1)
	state = mustAdvance(t, plan, state, map[string]bool{"has_material": true}, 101)
	if state.Branches["check"] != "then" || !state.Skipped["wait"] {
		t.Fatalf("then branch was not recorded: %#v", state)
	}
	assertReady(t, plan, state, "build")
	state = mustApply(t, plan, state, "build", 102, 1)
	assertReady(t, plan, state, "finish")

	other := mustApply(t, plan, State{}, "collect", 200, 1)
	other = mustAdvance(t, plan, other, map[string]bool{}, 201)
	if other.Branches["check"] != "else" || !other.Skipped["build"] {
		t.Fatalf("else branch was not recorded: %#v", other)
	}
	assertReady(t, plan, other, "wait")
}

func TestAdvanceRunsABoundedLoop(t *testing.T) {
	plan := Plan{
		ID:       "plan.loop",
		Revision: 1,
		Goal:     "repeat a bounded action",
		Budget:   Budget{MaxSteps: 8, MaxWorldMutations: 4, MaxTicks: 100},
		Nodes: []Node{
			{ID: "prepare", Kind: Action, Capability: "activity.wait", MaxAttempts: 1, Risk: "low"},
			{ID: "repeat", Kind: Action, Capability: "resource.harvest", MaxAttempts: 1, WorldMutations: 1, Risk: "high"},
			{ID: "loop", Kind: Loop, DependsOn: []string{"prepare"}, When: []string{"continue"}, Children: []string{"repeat"}, MaxAttempts: 1, MaxIterations: 2, Risk: "moderate"},
			{ID: "finish", Kind: Action, Capability: "activity.wait", DependsOn: []string{"loop"}, MaxAttempts: 1, Risk: "low"},
		},
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	state := mustApply(t, plan, State{}, "prepare", 1000, 0)
	state = mustAdvance(t, plan, state, map[string]bool{"continue": true}, 1001)
	assertReady(t, plan, state, "repeat")
	state = mustApply(t, plan, state, "repeat", 1002, 1)
	state = mustAdvance(t, plan, state, map[string]bool{"continue": true}, 1003)
	if state.Loops["loop"] != 1 || !state.ActiveLoops["loop"] {
		t.Fatalf("first loop iteration did not restart: %#v", state)
	}
	assertReady(t, plan, state, "repeat")
	state = mustApply(t, plan, state, "repeat", 1004, 1)
	state = mustAdvance(t, plan, state, map[string]bool{"continue": true}, 1005)
	if state.Loops["loop"] != 2 || !state.Completed["loop"] || state.ActiveLoops["loop"] ||
		!state.Completed["repeat"] || state.Attempts["repeat"] != 1 {
		t.Fatalf("loop bound was not enforced: %#v", state)
	}
	assertReady(t, plan, state, "finish")
	state = mustApply(t, plan, state, "finish", 1006, 0)
	if !plan.Done(state) || !plan.Succeeded(state) {
		t.Fatalf("completed loop plan was not terminal: %#v", state)
	}
}

func TestFailConsumesAttemptsAndStepBudget(t *testing.T) {
	plan := Plan{
		ID:       "plan.retry",
		Revision: 1,
		Goal:     "retry a bounded action",
		Budget:   Budget{MaxSteps: 3, MaxWorldMutations: 2, MaxTicks: 20},
		Nodes: []Node{{
			ID: "try", Kind: Action, Capability: "resource.harvest",
			MaxAttempts: 2, WorldMutations: 1, Risk: "high",
		}},
	}
	state, err := plan.Fail(State{}, "try", 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.Attempts["try"] != 1 || state.Steps != 1 {
		t.Fatalf("first failure was not recorded: %#v", state)
	}
	assertReady(t, plan, state, "try")
	state, err = plan.Fail(state, "try", 501, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ready := plan.Ready(state, nil); len(ready) != 0 || state.Attempts["try"] != 2 ||
		!state.Failed["try"] || !plan.Done(state) || plan.Succeeded(state) {
		t.Fatalf("attempt bound was not enforced: ready=%#v state=%#v", ready, state)
	}
	if _, err := plan.Fail(state, "try", 502, 0); err == nil {
		t.Fatal("exhausted action accepted another attempt")
	}
}

func TestApplyUsesAbsoluteTicksAndDoesNotMutateInput(t *testing.T) {
	plan := validPlan()
	before := State{}
	after := mustApply(t, plan, before, "collect", 50_000, 1)
	if before.Started || before.Steps != 0 || after.StartedAt != 50_000 || after.Steps != 1 {
		t.Fatalf("state was mutated or start tick was not captured: before=%#v after=%#v", before, after)
	}
	after = mustAdvance(t, plan, after, map[string]bool{"has_material": true}, 50_001)
	if _, err := plan.Advance(after, nil, 50_000); err == nil {
		t.Fatal("a backwards absolute tick was accepted")
	}
	if _, err := plan.Apply(after, "build", 56_001, 1); err == nil {
		t.Fatal("absolute tick budget was ignored")
	}
}

func TestValidateRejectsAmbiguousOrUnsafePlansAndStates(t *testing.T) {
	cycle := validPlan()
	cycle.Nodes[0].DependsOn = []string{"finish"}
	if err := cycle.Validate(); err == nil {
		t.Fatal("cycle was accepted")
	}
	unbounded := validPlan()
	unbounded.Nodes = append(unbounded.Nodes, Node{
		ID: "loop", Kind: Loop, Children: []string{"collect"},
		MaxAttempts: 1, Risk: "moderate",
	})
	if err := unbounded.Validate(); err == nil {
		t.Fatal("unbounded loop was accepted")
	}
	invalidRisk := validPlan()
	invalidRisk.Nodes[0].Risk = "unrestricted"
	if err := invalidRisk.Validate(); err == nil {
		t.Fatal("unknown risk was accepted")
	}
	ambiguous := validPlan()
	ambiguous.Nodes[1].Else = []string{"build"}
	if err := ambiguous.Validate(); err == nil {
		t.Fatal("node controlled by two branch paths was accepted")
	}
	if err := validPlan().ValidateState(State{
		Attempts: map[string]uint32{"missing": 1},
	}); err == nil {
		t.Fatal("state for an unknown node was accepted")
	}
	forged := State{
		Started: true,
		Skipped: map[string]bool{"collect": true},
	}
	if err := validPlan().ValidateState(forged); err == nil {
		t.Fatal("a root action was forged as skipped")
	}
}

func ExamplePlan_Advance() {
	plan := validPlan()
	state, _ := plan.Apply(State{}, "collect", 100, 1)
	state, _ = plan.Advance(state, map[string]bool{"has_material": true}, 101)
	fmt.Println(state.Branches["check"], plan.Ready(state, nil)[0].ID)
	// Output: then build
}

func assertReady(t *testing.T, plan Plan, state State, expected ...string) {
	t.Helper()
	ready := plan.Ready(state, nil)
	if len(ready) != len(expected) {
		t.Fatalf("ready=%#v, expected=%#v", ready, expected)
	}
	for index, id := range expected {
		if ready[index].ID != id {
			t.Fatalf("ready[%d]=%q, expected=%q", index, ready[index].ID, id)
		}
	}
}

func mustApply(
	t *testing.T,
	plan Plan,
	state State,
	nodeID string,
	tick uint64,
	mutations uint32,
) State {
	t.Helper()
	next, err := plan.Apply(state, nodeID, tick, mutations)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mustAdvance(
	t *testing.T,
	plan Plan,
	state State,
	facts map[string]bool,
	tick uint64,
) State {
	t.Helper()
	next, err := plan.Advance(state, facts, tick)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

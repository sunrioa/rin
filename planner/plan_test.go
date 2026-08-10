package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func validPlan() Plan {
	return Plan{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "plan.survival",
		Revision:      1,
		Goal:          "prepare survival basics",
		Budget:        Budget{MaxSteps: 32, MaxWorldMutations: 16, MaxTicks: 6000},
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
		SchemaVersion: CurrentSchemaVersion,
		ID:            "plan.loop",
		Revision:      1,
		Goal:          "repeat a bounded action",
		Budget:        Budget{MaxSteps: 8, MaxWorldMutations: 4, MaxTicks: 100},
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
	if state.RetiredSteps != 1 {
		t.Fatalf("completed loop attempts were not retired: %#v", state)
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

func TestNestedLoopsRetireCompletedAttempts(t *testing.T) {
	plan := Plan{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "plan.nested-loop",
		Revision:      1,
		Goal:          "validate nested loop accounting",
		Budget:        Budget{MaxSteps: 16, MaxWorldMutations: 0, MaxTicks: 100},
		Nodes: []Node{
			{ID: "repeat", Kind: Action, Capability: "activity.wait",
				MaxAttempts: 1, Risk: "low"},
			{ID: "inner", Kind: Loop, When: []string{"continue_inner"},
				Children: []string{"repeat"}, MaxAttempts: 1,
				MaxIterations: 2, Risk: "low"},
			{ID: "outer", Kind: Loop, When: []string{"continue_outer"},
				Children: []string{"inner"}, MaxAttempts: 1,
				MaxIterations: 2, Risk: "low"},
		},
	}
	facts := map[string]bool{"continue_inner": true, "continue_outer": true}
	state := mustAdvance(t, plan, State{}, facts, 100)
	for iteration := 0; iteration < 4; iteration++ {
		assertReady(t, plan, state, "repeat")
		state = mustApply(t, plan, state, "repeat", uint64(101+iteration*2), 0)
		state = mustAdvance(t, plan, state, facts, uint64(102+iteration*2))
	}
	if !state.Completed["outer"] || !state.Completed["inner"] ||
		state.Loops["outer"] != 2 || state.Loops["inner"] != 2 ||
		state.Steps != 4 || state.Attempts["repeat"] != 1 ||
		state.RetiredSteps != 3 || !plan.Done(state) || !plan.Succeeded(state) {
		t.Fatalf("nested loop accounting is inconsistent: %#v", state)
	}
	forged := cloneState(state)
	forged.Steps++
	forged.RetiredSteps++
	if err := plan.ValidateState(forged); err == nil {
		t.Fatal("nested loop accepted retired steps beyond its execution history")
	}
}

func TestAdvanceSkipsLoopBodyWhenConditionIsFalse(t *testing.T) {
	plan := Plan{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "plan.zero-loop",
		Revision:      1,
		Goal:          "continue after an optional loop",
		Budget:        Budget{MaxSteps: 2, MaxWorldMutations: 0, MaxTicks: 20},
		Nodes: []Node{
			{ID: "repeat", Kind: Action, Capability: "activity.wait",
				MaxAttempts: 1, Risk: "low"},
			{ID: "loop", Kind: Loop, When: []string{"continue"},
				Children: []string{"repeat"}, MaxAttempts: 1,
				MaxIterations: 2, Risk: "low"},
			{ID: "finish", Kind: Action, Capability: "activity.wait",
				DependsOn: []string{"repeat"}, MaxAttempts: 1, Risk: "low"},
		},
	}
	forged := State{
		Started:   true,
		StartedAt: 100,
		Tick:      100,
		Completed: map[string]bool{"loop": true},
	}
	if err := plan.ValidateState(forged); err == nil {
		t.Fatal("zero-iteration loop accepted an unaccounted child subtree")
	}
	state := mustAdvance(t, plan, State{}, map[string]bool{}, 100)
	if !state.Completed["loop"] || !state.Skipped["repeat"] || state.Loops["loop"] != 0 {
		t.Fatalf("zero-iteration loop did not skip its body: %#v", state)
	}
	assertReady(t, plan, state, "finish")
	state = mustApply(t, plan, state, "finish", 101, 0)
	if !plan.Done(state) || !plan.Succeeded(state) {
		t.Fatalf("plan deadlocked after a zero-iteration loop: %#v", state)
	}
}

func TestFailConsumesAttemptsAndStepBudget(t *testing.T) {
	plan := Plan{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "plan.retry",
		Revision:      1,
		Goal:          "retry a bounded action",
		Budget:        Budget{MaxSteps: 3, MaxWorldMutations: 2, MaxTicks: 20},
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
	unicodeGoal := validPlan()
	unicodeGoal.Goal = strings.Repeat("界", MaxConditionLen)
	if err := unicodeGoal.Validate(); err != nil {
		t.Fatalf("Unicode goal used bytes instead of characters: %v", err)
	}
	unicodeGoal.Goal += "界"
	if err := unicodeGoal.Validate(); err == nil {
		t.Fatal("overlong Unicode goal was accepted")
	}
	wrongVersion := validPlan()
	wrongVersion.SchemaVersion++
	if err := wrongVersion.Validate(); err == nil {
		t.Fatal("unsupported plan schema version was accepted")
	}
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
	deadlockedLoop := Plan{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "plan.deadlocked-loop",
		Revision:      1,
		Goal:          "reject a child waiting for its active loop",
		Budget:        Budget{MaxSteps: 4, MaxWorldMutations: 0, MaxTicks: 20},
		Nodes: []Node{
			{ID: "repeat", Kind: Action, Capability: "activity.wait",
				DependsOn: []string{"loop"}, MaxAttempts: 1, Risk: "low"},
			{ID: "loop", Kind: Loop, Children: []string{"repeat"},
				MaxAttempts: 1, MaxIterations: 2, Risk: "low"},
		},
	}
	if err := deadlockedLoop.Validate(); err == nil {
		t.Fatal("a loop child depending on its unfinished loop was accepted")
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
	duplicateDependency := validPlan()
	duplicateDependency.Nodes[2].DependsOn = []string{"check", "check"}
	if err := duplicateDependency.Validate(); err == nil {
		t.Fatal("duplicate dependency was accepted")
	}
	tooManyConditions := validPlan()
	tooManyConditions.Nodes[1].When = make([]string, MaxConditions+1)
	for index := range tooManyConditions.Nodes[1].When {
		tooManyConditions.Nodes[1].When[index] = fmt.Sprintf("fact_%d", index)
	}
	if err := tooManyConditions.Validate(); err == nil {
		t.Fatal("unbounded condition list was accepted")
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
	forgedBranch := State{
		Started:   true,
		StartedAt: 1,
		Tick:      1,
		Completed: map[string]bool{"check": true},
		Branches:  map[string]string{"check": "then"},
	}
	if err := validPlan().ValidateState(forgedBranch); err == nil {
		t.Fatal("a branch was completed before its dependency")
	}
	forgedSteps := State{
		Started:   true,
		StartedAt: 1,
		Tick:      1,
		Attempts:  map[string]uint32{"collect": 1},
	}
	if err := validPlan().ValidateState(forgedSteps); err == nil {
		t.Fatal("attempts without matching step count were accepted")
	}
	forgedStepBudget := State{
		Started:   true,
		StartedAt: 1,
		Tick:      1,
		Steps:     validPlan().Budget.MaxSteps,
	}
	if err := validPlan().ValidateState(forgedStepBudget); err == nil {
		t.Fatal("step budget without matching attempts was accepted")
	}
	loopPlan := Plan{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "plan.loop-state",
		Revision:      1,
		Goal:          "validate persisted loop state",
		Budget:        Budget{MaxSteps: 4, MaxWorldMutations: 0, MaxTicks: 20},
		Nodes: []Node{
			{ID: "repeat", Kind: Action, Capability: "activity.wait",
				MaxAttempts: 1, Risk: "low"},
			{ID: "loop", Kind: Loop, Children: []string{"repeat"},
				MaxAttempts: 1, MaxIterations: 2, Risk: "low"},
		},
	}
	if err := loopPlan.ValidateState(State{
		Started: true, StartedAt: 1, Tick: 1,
		Loops: map[string]uint32{"loop": 1},
	}); err == nil {
		t.Fatal("an unreachable between-iterations loop state was accepted")
	}
}

func TestStepBudgetExhaustionIsTerminalButNotSuccessful(t *testing.T) {
	plan := validPlan()
	plan.Budget.MaxSteps = 1
	state := mustApply(t, plan, State{}, "collect", 100, 1)
	if ready := plan.Ready(state, nil); len(ready) != 0 {
		t.Fatalf("step-exhausted plan returned ready actions: %#v", ready)
	}
	if !plan.Done(state) || plan.Succeeded(state) {
		t.Fatalf("step-exhausted incomplete plan has wrong terminal state: %#v", state)
	}
}

func TestPlanV1FixtureMatchesSchemaAndRuntime(t *testing.T) {
	schemaDocument, err := os.ReadFile("schema/plan-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/plan-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	compiled := compileFixtureSchema(t, schemaDocument)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(instance); err != nil {
		t.Fatalf("plan fixture does not match plan-v1 schema: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(fixture))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan fixture does not match runtime validation: %v", err)
	}
	assertReady(t, plan, State{}, "collect")

	duplicate := validPlan()
	duplicate.Nodes[2].DependsOn = []string{"check", "check"}
	encoded, err := json.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	invalidInstance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(invalidInstance); err == nil {
		t.Fatal("plan-v1 schema accepted duplicate references")
	}
}

func compileFixtureSchema(t *testing.T, document []byte) *jsonschema.Schema {
	t.Helper()
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const location = "https://sunrioa.github.io/rin/schema/planner/plan-v1.schema.json"
	if err := compiler.AddResource(location, value); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
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

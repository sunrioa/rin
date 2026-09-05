package cognition_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

func TestUnknownTaskReconcilesLateHostOutcomeAndUnblocksSignals(t *testing.T) {
	ctx := context.Background()
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision(), {Kind: cognition.ModelDecisionComplete, Summary: "Finished after reconciliation."}}
	unknown := queuedAgentOperation()
	unknown.Status = controlplane.OperationOutcomeUnknown
	unknown.ReconciliationPending = true
	unknown.Terminal = false
	unknown.DeliveryAttempts = 1
	fixture.control.operationAfterSubmit = unknown
	runtime := fixture.runtime(t, 16)
	started := fixture.start(t, runtime, "task.old-unknown")
	stopped, err := runtime.RunTask(ctx, started.TaskID)
	if err != nil || stopped.Status != cognition.TaskOutcomeUnknown {
		t.Fatalf("unknown setup: %s %v", stopped.Status, err)
	}
	settled := succeededAgentOperation(fixture.environment.observation)
	settled.OperationID = unknown.OperationID
	fixture.control.operationAfterSubmit = settled
	if ready, err := runtime.TaskReady(ctx, stopped); err != nil || !ready {
		t.Fatalf("settled task did not wake: %v %v", ready, err)
	}
	current, err := runtime.RunTask(ctx, stopped.TaskID)
	if err != nil || current.Status != cognition.TaskActive || current.PendingAction != nil {
		t.Fatalf("reconciliation failed: %#v %v", current, err)
	}
	if len(fixture.control.submissions) != 1 {
		t.Fatal("reconciliation resubmitted the action")
	}
	current, err = runtime.RunTask(ctx, stopped.TaskID)
	if err != nil || current.Status != cognition.TaskCompleted {
		t.Fatalf("task did not finish: %s %v", current.Status, err)
	}
	result, err := runtime.HandleActorSignal(ctx, actorSignal(fixture, "signal.after-settlement", false))
	if err != nil || result.Status != "started" {
		t.Fatalf("old unknown still blocked the actor: %#v %v", result, err)
	}

}

type architectureCrashPlanClient struct {
	taskstate.PlanClient
	store *taskstate.Store
	crash bool
}

func (client *architectureCrashPlanClient) GetPlan(ctx context.Context, id string) (taskstate.PlanState, error) {
	return client.store.Get(ctx, id)
}

const architectureCrashPoint = "audit: stopped after durable Plan creation"

func (client *architectureCrashPlanClient) CreatePlan(ctx context.Context, draft taskstate.Draft) (taskstate.PlanState, error) {
	plan, err := client.store.Create(ctx, draft)
	if err == nil && client.crash {
		panic(architectureCrashPoint)
	}
	return plan, err
}

func TestPlanReferenceRecoveredAcrossCommitWindowAndRestart(t *testing.T) {
	ctx := context.Background()
	fixture := newAgentRuntimeFixture(t)
	dir := t.TempDir()
	taskPath, planPath := filepath.Join(dir, "tasks.db"), filepath.Join(dir, "taskstate.db")
	tasks, err := cognition.OpenSQLiteTaskStore(taskPath, 10)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := taskstate.OpenSQLiteStore(planPath, taskstate.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	client := &architectureCrashPlanClient{PlanClient: &runtimePlanStub{control: fixture.control, principal: fixture.principal}, store: plans, crash: true}
	makeRuntime := func() *cognition.AgentRuntime {
		runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
			Principal: fixture.principal, Control: fixture.control, Environment: fixture.environment,
			Persona: fixture.persona, Memory: fixture.memory, Skills: fixture.skills, Model: fixture.model,
			Tasks: tasks, Decisions: fixture.decisions, Plans: client, Now: fixture.now, MaxAdvancesPerRun: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	action := agentActionDecision()
	action.PlanDraft = &taskstate.Draft{Phase: "Approach", MaxReplans: 2, Steps: []taskstate.StepDraft{{
		StepID: "step.approach", Title: "Approach", Objective: "Reach the player.", MaxAttempts: 3,
		CapabilityHints:   []host.CapabilityRef{action.Capability},
		SuccessConditions: []taskstate.PlanCondition{{ConditionID: "condition.arrived", Kind: taskstate.EvidenceOperationOutcome, Summary: "Host confirms arrival.", Capability: &action.Capability}},
	}}}
	fixture.model.decisions = []cognition.ModelDecision{action, agentActionDecision()}
	runtime := makeRuntime()
	started, err := runtime.StartTask(ctx, cognition.StartTaskInput{TaskID: "task.crash-gap", HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira", ControllerID: "controller.internal", Goal: "Reach the nearby player.", PlanningMode: taskstate.PlanningRequired})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if value := recover(); value != architectureCrashPoint {
				t.Fatalf("wrong crash point: %#v", value)
			}
		}()
		_, _ = runtime.RunTask(ctx, started.TaskID)
	}()
	if err := tasks.Close(); err != nil {
		t.Fatal(err)
	}
	if err := plans.Close(); err != nil {
		t.Fatal(err)
	}
	tasks, err = cognition.OpenSQLiteTaskStore(taskPath, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tasks.Close()
	plans, err = taskstate.OpenSQLiteStore(planPath, taskstate.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer plans.Close()
	client.store, client.crash = plans, false
	restored, err := tasks.Load(ctx, started.TaskID)
	if err != nil || restored.PlanID != "" {
		t.Fatalf("unexpected Task reference: %q %v", restored.PlanID, err)
	}
	if _, err := plans.Get(ctx, "plan."+started.TaskID); err != nil {
		t.Fatalf("committed Plan missing: %v", err)
	}
	runtime = makeRuntime()
	current, err := runtime.RunTask(ctx, started.TaskID)
	if err != nil || current.Status != cognition.TaskActive || current.PlanID != "plan."+started.TaskID || current.PendingAction == nil {
		t.Fatalf("plan was not recovered: status=%s plan=%s err=%v", current.Status, current.PlanID, err)
	}
	if input := fixture.model.inputs[len(fixture.model.inputs)-1]; input.Plan == nil || input.Plan.PlanID != current.PlanID {
		t.Fatal("recovered plan was not given to the model")
	}
}

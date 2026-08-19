package mcpbridge

import (
	"context"
	"slices"
	"testing"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

func TestGatewayExposesSharedTaskPlansByScope(t *testing.T) {
	service := controlplane.New(controlplane.Options{})
	defer service.Close()
	principal := actionPrincipal()
	controlClient, err := controlplane.NewClientService(service, principal)
	if err != nil {
		t.Fatal(err)
	}
	plans := &testPlanClient{}
	gateway, err := NewClientWithSkillsAndPlans(controlClient, nil, plans, principal)
	if err != nil {
		t.Fatal(err)
	}
	session := connectGateway(t, gateway)
	listedTools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(listedTools.Tools)
	for _, name := range []string{
		"create_task_plan", "get_task_plan", "wait_task_plan_update",
		"revise_task_plan", "request_task_step_transition",
		"pause_task_plan", "resume_task_plan", "cancel_task_plan",
		"submit_task_step_action",
	} {
		if !slices.Contains(names, name) {
			t.Fatalf("tool %q is absent from %#v", name, names)
		}
	}
	epoch := host.Epoch{
		SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
	}
	var created PlanOutput
	callTool(t, session, "create_task_plan", map[string]any{
		"plan_id": "plan.mcp", "task_id": "task.mcp", "session_id": "session.one",
		"host_id": "host.one", "world_id": "world.one", "actor_id": "actor.one",
		"controller_id": "controller.one", "controller_source": "external",
		"goal": "Collect material and return home.", "planning_mode": "auto",
		"steps": []map[string]any{{
			"step_id": "step.collect", "title": "Collect", "objective": "Collect material.",
			"max_attempts":     3,
			"capability_hints": []map[string]any{{"id": "resource.harvest", "version": "1.0.0"}},
			"success_conditions": []map[string]any{{
				"condition_id": "condition.collected", "kind": "operation-outcome",
				"summary":         "Host confirms collection.",
				"capability":      map[string]any{"id": "resource.harvest", "version": "1.0.0"},
				"fact_id":         "",
				"fact_value_json": "",
			}},
		}},
		"based_on_epoch": epoch, "based_on_observation_sequence": 1,
	}, &created)
	if created.Plan.PlanID != "plan.mcp" || plans.created != 1 {
		t.Fatalf("created = %#v, calls=%d", created, plans.created)
	}
	var fetched PlanOutput
	callTool(t, session, "get_task_plan", map[string]any{"plan_id": "plan.mcp"}, &fetched)
	if fetched.Plan.Revision != created.Plan.Revision {
		t.Fatalf("fetched = %#v", fetched)
	}
}

type testPlanClient struct {
	plan    taskstate.PlanState
	created int
}

func (client *testPlanClient) CreatePlan(_ context.Context, input taskstate.Draft) (taskstate.PlanState, error) {
	client.created++
	plan, err := taskstate.NewPlan(input, 1)
	client.plan = plan
	return plan, err
}

func (client *testPlanClient) GetPlan(context.Context, string) (taskstate.PlanState, error) {
	return client.plan, nil
}

func (client *testPlanClient) WaitPlan(context.Context, taskstate.WaitInput) (taskstate.PlanUpdate, error) {
	return taskstate.PlanUpdate{Plan: client.plan}, nil
}

func (client *testPlanClient) RevisePlan(context.Context, taskstate.ReviseInput) (taskstate.PlanState, error) {
	return client.plan, nil
}

func (client *testPlanClient) SetPlanStatus(context.Context, taskstate.StatusInput) (taskstate.PlanState, error) {
	return client.plan, nil
}

func (client *testPlanClient) RequestTransition(context.Context, taskstate.TransitionInput) (taskstate.PlanState, error) {
	return client.plan, nil
}

func (client *testPlanClient) SubmitStepAction(context.Context, taskstate.SubmitStepActionInput) (controlplane.OperationView, error) {
	return controlplane.OperationView{OperationID: "operation.plan"}, nil
}

package managementapi

import (
	"context"
	"slices"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

type fakePlanReader struct {
	plan taskstate.PlanState
}

func (reader fakePlanReader) Get(context.Context, string) (taskstate.PlanState, error) {
	return reader.plan, nil
}

func (reader fakePlanReader) List(context.Context) ([]taskstate.PlanState, error) {
	return []taskstate.PlanState{reader.plan}, nil
}

type fakeTaskManager struct {
	task          cognition.TaskSession
	timelineQuery timeline.Query
}

func (manager *fakeTaskManager) StartTask(
	_ context.Context,
	input cognition.StartTaskInput,
) (cognition.TaskSession, error) {
	manager.task = cognition.TaskSession{
		TaskID: input.TaskID, HostID: input.HostID, WorldID: input.WorldID,
		ActorID: input.ActorID, ControllerID: input.ControllerID, Goal: input.Goal,
		Tags:         append([]string(nil), input.Tags...),
		PlanningMode: input.PlanningMode, Status: cognition.TaskActive,
		Budget: input.Budget,
	}
	return manager.task, nil
}

func (manager *fakeTaskManager) SnapshotTasks(context.Context) (cognition.TaskSnapshot, error) {
	return cognition.TaskSnapshot{Revision: 2, Tasks: []cognition.TaskSession{manager.task}}, nil
}

func (manager *fakeTaskManager) GetTask(context.Context, string) (cognition.TaskSession, error) {
	return manager.task, nil
}

func (manager *fakeTaskManager) GetTaskTimeline(_ context.Context, query timeline.Query) (timeline.Page, error) {
	manager.timelineQuery = query
	return timeline.Page{
		ContractVersion: timeline.ContractVersion, TaskID: manager.task.TaskID,
		Events:     []timeline.Event{{TaskID: manager.task.TaskID, PublicSummary: "Observed outcome."}},
		NextCursor: query.AfterCursor, More: true,
	}, nil
}

func (manager *fakeTaskManager) RunTask(context.Context, string) (cognition.TaskSession, error) {
	manager.task.Status = cognition.TaskActive
	return manager.task, nil
}

func (manager *fakeTaskManager) ResumeTask(context.Context, string) (cognition.TaskSession, error) {
	manager.task.Status = cognition.TaskActive
	return manager.task, nil
}

func (manager *fakeTaskManager) CancelTask(context.Context, string) (cognition.TaskSession, error) {
	manager.task.Status = cognition.TaskCancelling
	return manager.task, nil
}

func TestTaskManagementReturnsSafeSummaryAndPublicTimeline(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeTaskManager{task: cognition.TaskSession{
		TaskID: "task.console", HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		Goal: "Collect supplies.", Status: cognition.TaskPaused, PauseCode: "host.offline",
		PlanID: "plan.console", PlanRevision: 2, CurrentPlanStepID: "step.collect",
		Budget: cognition.TaskBudget{MaxSteps: 48}, UpdatedAtUnixMillis: 100,
	}}
	service, err := New(personas, memory, manager)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := taskstate.NewPlan(taskstate.Draft{
		PlanID: "plan.console", TaskID: manager.task.TaskID, SessionID: manager.task.TaskID,
		HostID: manager.task.HostID, WorldID: manager.task.WorldID, ActorID: manager.task.ActorID,
		ControllerID: "controller.rin-console", ControllerSource: taskstate.ControllerInternal,
		Goal: manager.task.Goal, PlanningMode: taskstate.PlanningRequired,
		BasedOnEpoch:               host.Epoch{SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1},
		BasedOnObservationSequence: 1,
		Steps: []taskstate.StepDraft{{
			StepID: "step.collect", Title: "Collect", Objective: "Collect supplies.",
			CapabilityHints: []host.CapabilityRef{{ID: "resource.harvest", Version: "1.0.0"}},
			SuccessConditions: []taskstate.PlanCondition{{
				ConditionID: "condition.collect", Kind: taskstate.EvidenceOperationOutcome,
				Summary:    "Supplies were collected.",
				Capability: &host.CapabilityRef{ID: "resource.harvest", Version: "1.0.0"},
			}},
		}},
	}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigurePlans(fakePlanReader{plan: plan}); err != nil {
		t.Fatal(err)
	}
	list, err := service.ListTasks(context.Background(), TaskListInput{})
	if err != nil || len(list.Tasks) != 1 || list.Tasks[0].Goal != manager.task.Goal {
		t.Fatalf("task list = %#v, %v", list, err)
	}
	detail, err := service.GetTask(context.Background(), TaskGetInput{
		TaskID: manager.task.TaskID, AfterCursor: "cursor-24", Limit: 80,
	})
	if err != nil || len(detail.Timeline.Events) != 1 || detail.Task.PauseCode != "host.offline" ||
		detail.Plan == nil || detail.Plan.CurrentStepID != "step.collect" {
		t.Fatalf("task detail = %#v, %v", detail, err)
	}
	if manager.timelineQuery.TaskID != manager.task.TaskID ||
		manager.timelineQuery.AfterCursor != "cursor-24" || manager.timelineQuery.Limit != 80 {
		t.Fatalf("timeline query = %#v", manager.timelineQuery)
	}
	cancelled, err := service.ControlTask(context.Background(), TaskControlInput{
		TaskID: manager.task.TaskID, Action: "cancel",
	})
	if err != nil || cancelled.Status != string(cognition.TaskCancelling) {
		t.Fatalf("cancelled task = %#v, %v", cancelled, err)
	}
	started, err := service.StartTask(context.Background(), TaskStartInput{
		TaskID: "task.console-start", HostID: "host.one", WorldID: "world.one",
		ActorID: "actor.one", Goal: "Reach the End and defeat the dragon.",
		Tags: []string{"minecraft.ender-dragon", "long-goal"},
	})
	if err != nil || started.PlanningMode != "required" || started.Status != "active" ||
		manager.task.ControllerID != "controller.rin-console" || manager.task.Budget.MaxActions != 512 ||
		!slices.Equal(manager.task.Tags,
			[]string{"console", "long-goal", "minecraft.ender-dragon"}) ||
		!slices.Equal(started.Tags, manager.task.Tags) {
		t.Fatalf("started task = %#v, stored = %#v, %v", started, manager.task, err)
	}
}

func TestTaskManagementListsExternalMCPPlansWithoutInternalRuntime(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := taskstate.NewPlan(taskstate.Draft{
		PlanID: "plan.external", TaskID: "task.external", SessionID: "session.external",
		HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		ControllerID: "controller.mcp", ControllerSource: taskstate.ControllerExternal,
		Goal: "Collect supplies without the internal runtime.", PlanningMode: taskstate.PlanningRequired,
		BasedOnEpoch: host.Epoch{
			SessionID: "session.external", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
		},
		BasedOnObservationSequence: 1,
		Steps: []taskstate.StepDraft{{
			StepID: "step.collect", Title: "Collect", Objective: "Collect supplies.",
			CapabilityHints: []host.CapabilityRef{{ID: "resource.harvest", Version: "1.0.0"}},
			SuccessConditions: []taskstate.PlanCondition{{
				ConditionID: "condition.collect", Kind: taskstate.EvidenceOperationOutcome,
				Summary:    "The Host confirms collection.",
				Capability: &host.CapabilityRef{ID: "resource.harvest", Version: "1.0.0"},
			}},
		}},
	}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigurePlans(fakePlanReader{plan: plan}); err != nil {
		t.Fatal(err)
	}
	list, err := service.ListTasks(context.Background(), TaskListInput{})
	if err != nil || len(list.Tasks) != 1 || list.Tasks[0].TaskControlAvailable ||
		list.Tasks[0].ControllerSource != "external" || list.Tasks[0].PlanID != plan.PlanID {
		t.Fatalf("external plan list = %#v, %v", list, err)
	}
	detail, err := service.GetTask(context.Background(), TaskGetInput{TaskID: plan.PlanID})
	if err != nil || detail.Plan == nil || detail.Plan.PlanID != plan.PlanID ||
		detail.Task.TaskControlAvailable || detail.Timeline.ContractVersion != timeline.ContractVersion {
		t.Fatalf("external plan detail = %#v, %v", detail, err)
	}
}

package managementapi

import (
	"context"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/timeline"
)

type fakeTaskManager struct {
	task cognition.TaskSession
}

func (manager *fakeTaskManager) SnapshotTasks(context.Context) (cognition.TaskSnapshot, error) {
	return cognition.TaskSnapshot{Revision: 2, Tasks: []cognition.TaskSession{manager.task}}, nil
}

func (manager *fakeTaskManager) GetTask(context.Context, string) (cognition.TaskSession, error) {
	return manager.task, nil
}

func (manager *fakeTaskManager) GetTaskTimeline(context.Context, timeline.Query) (timeline.Page, error) {
	return timeline.Page{
		ContractVersion: timeline.ContractVersion, TaskID: manager.task.TaskID,
		Events: []timeline.Event{{TaskID: manager.task.TaskID, PublicSummary: "Observed outcome."}},
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
		Budget: cognition.TaskBudget{MaxSteps: 48}, UpdatedAtUnixMillis: 100,
	}}
	service, err := New(personas, memory, manager)
	if err != nil {
		t.Fatal(err)
	}
	list, err := service.ListTasks(context.Background(), TaskListInput{})
	if err != nil || len(list.Tasks) != 1 || list.Tasks[0].Goal != manager.task.Goal {
		t.Fatalf("task list = %#v, %v", list, err)
	}
	detail, err := service.GetTask(context.Background(), TaskGetInput{TaskID: manager.task.TaskID})
	if err != nil || len(detail.Timeline.Events) != 1 || detail.Task.PauseCode != "host.offline" {
		t.Fatalf("task detail = %#v, %v", detail, err)
	}
	cancelled, err := service.ControlTask(context.Background(), TaskControlInput{
		TaskID: manager.task.TaskID, Action: "cancel",
	})
	if err != nil || cancelled.Status != string(cognition.TaskCancelling) {
		t.Fatalf("cancelled task = %#v, %v", cancelled, err)
	}
}

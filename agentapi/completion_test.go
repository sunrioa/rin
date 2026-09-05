package agentapi_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
)

type reviewTaskRuntime struct {
	*fakeTaskRuntime
	confirmCalls int
}

func (runtime *reviewTaskRuntime) ConfirmTaskCompletion(ctx context.Context, id string, revision uint64) (cognition.TaskSession, error) {
	task, err := runtime.GetTask(ctx, id)
	if err != nil {
		return task, err
	}
	if task.Revision != revision {
		return task, cognition.ErrTaskRevisionConflict
	}
	runtime.confirmCalls++
	task.Status = cognition.TaskCompleted
	return task, nil
}

func TestCompletionConfirmationUsesExecuteScopeAndExactRevisionOverHTTP(t *testing.T) {
	runtime := &reviewTaskRuntime{fakeTaskRuntime: newFakeTaskRuntime()}
	task := activeTask("task.review", "task.completion-requested")
	task.Revision = 5
	task.Status = cognition.TaskPaused
	task.Schedule = cognition.TaskSchedule{Kind: cognition.ScheduleUser}
	runtime.tasks[task.TaskID] = task
	service, err := agentapi.New(agentapi.Options{Runtime: runtime, WorkerCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	input := agentapi.CompletionConfirmationInput{TaskID: task.TaskID, ExpectedRevision: task.Revision}
	if _, err := service.ConfirmTaskCompletion(context.Background(), taskPrincipal(agentapi.ScopeTaskRead), input); !errors.Is(err, agentapi.ErrForbidden) {
		t.Fatalf("read scope confirmed completion: %v", err)
	}
	handler, err := agentapi.NewHTTPHandler(service, agentapi.HTTPOptions{Token: testAgentToken, ClientPrincipal: taskPrincipal(agentapi.ScopeTaskExecute)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := agentapi.NewHTTPClient(server.URL, testAgentToken)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision--
	if _, err := client.ConfirmTaskCompletion(context.Background(), input); !errors.Is(err, agentapi.ErrConflict) {
		t.Fatalf("stale HTTP confirmation = %v", err)
	}
	input.ExpectedRevision++
	result, err := client.ConfirmTaskCompletion(context.Background(), input)
	if err != nil || result.Task.Status != cognition.TaskCompleted || result.Scheduled || runtime.confirmCalls != 1 {
		t.Fatalf("confirmation = %#v %v calls=%d", result, err, runtime.confirmCalls)
	}
}

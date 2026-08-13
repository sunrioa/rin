package agentapi_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/timeline"
)

const testAgentToken = "0123456789abcdef0123456789abcdef"

func TestHTTPClientUsesDaemonBoundPrincipalAndTaskContract(t *testing.T) {
	runtime := newFakeTaskRuntime()
	service := newTestAgentService(t, runtime, 1)
	defer service.Close()
	principal := taskPrincipal(
		agentapi.ScopeTaskRead,
		agentapi.ScopeTaskExecute,
		agentapi.ScopeTaskCancel,
	)
	handler, err := agentapi.NewHTTPHandler(service, agentapi.HTTPOptions{
		Token: testAgentToken, ClientPrincipal: principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := agentapi.NewHTTPClient(server.URL, testAgentToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	info, err := client.Info(ctx)
	if err != nil || info.ContractVersion != agentapi.ContractVersion ||
		!reflect.DeepEqual(info.Principal, principal) {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	principal.GrantedScopes[0] = "forged.scope"
	again, err := client.Info(ctx)
	if err != nil || again.Principal.GrantedScopes[0] != agentapi.ScopeTaskRead {
		t.Fatalf("daemon principal was mutable: %+v, %v", again, err)
	}

	started, err := client.StartTask(ctx, startTaskInput("task.http"))
	if err != nil || started.Task.TaskID != "task.http" || !started.Scheduled {
		t.Fatalf("StartTask = %+v, %v", started, err)
	}
	waitFor(t, func() bool {
		return runtime.runCount("task.http") == 1 && runtime.startedCount() == 0
	}, "initial HTTP task run")
	time.Sleep(10 * time.Millisecond)
	stored, err := client.GetTask(ctx, "task.http")
	if err != nil || stored.TaskID != "task.http" ||
		!reflect.DeepEqual(stored.AllowedCapabilities, []string{"dialogue.speak"}) {
		t.Fatalf("GetTask = %+v, %v", stored, err)
	}
	taskTimeline, err := client.GetTaskTimeline(ctx, timeline.Query{TaskID: "task.http"})
	if err != nil || taskTimeline.ContractVersion != timeline.ContractVersion ||
		len(taskTimeline.Events) != 2 {
		t.Fatalf("GetTaskTimeline = %+v, %v", taskTimeline, err)
	}
	timelineUpdate, err := client.WaitTaskTimeline(ctx, timeline.WaitInput{
		TaskID: "task.http", AfterCursor: taskTimeline.NextCursor, WaitMillis: 0,
	})
	if err != nil || timelineUpdate.Changed || len(timelineUpdate.Timeline.Events) != 0 {
		t.Fatalf("WaitTaskTimeline = %+v, %v", timelineUpdate, err)
	}
	if _, err := client.RunTask(ctx, "task.http"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	waitFor(t, func() bool {
		return runtime.runCount("task.http") == 2 && runtime.startedCount() == 0
	}, "explicit HTTP task run")
	time.Sleep(10 * time.Millisecond)
	cancelled, err := client.CancelTask(ctx, "task.http")
	if err != nil || cancelled.Task.Status != "cancelled" {
		t.Fatalf("CancelTask = %+v, %v", cancelled, err)
	}
}

func TestTaskHTTPRejectsCredentialAndPrincipalForgery(t *testing.T) {
	runtime := newFakeTaskRuntime()
	service := newTestAgentService(t, runtime, 1)
	defer service.Close()
	handler, err := agentapi.NewHTTPHandler(service, agentapi.HTTPOptions{
		Token: testAgentToken,
		ClientPrincipal: taskPrincipal(
			agentapi.ScopeTaskRead,
			agentapi.ScopeTaskExecute,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	wrong, err := agentapi.NewHTTPClient(
		server.URL,
		"fedcba9876543210fedcba9876543210",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Info(context.Background()); !errors.Is(err, agentapi.ErrForbidden) {
		t.Fatalf("wrong token error = %v", err)
	}

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/agent/v1/tasks/get",
		strings.NewReader(`{"task_id":"task.http","principal":{"id":"admin"}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testAgentToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("principal forgery status = %d", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), "admin") {
		t.Fatalf("forged principal was reflected: %s", body)
	}
}

func TestTaskHTTPMapsValidationNotFoundAndBodyLimit(t *testing.T) {
	runtime := newFakeTaskRuntime()
	service := newTestAgentService(t, runtime, 1)
	defer service.Close()
	principal := taskPrincipal(agentapi.ScopeTaskRead, agentapi.ScopeTaskExecute)
	handler, err := agentapi.NewHTTPHandler(service, agentapi.HTTPOptions{
		Token: testAgentToken, ClientPrincipal: principal, MaxBodyBytes: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := agentapi.NewHTTPClient(server.URL, testAgentToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetTask(context.Background(), "Missing Task"); !errors.Is(err, agentapi.ErrInvalid) {
		t.Fatalf("invalid task ID error = %v", err)
	}
	if _, err := client.GetTask(context.Background(), "task.missing"); !errors.Is(err, agentapi.ErrNotFound) {
		t.Fatalf("missing task error = %v", err)
	}

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/agent/v1/tasks/start",
		bytes.NewReader(bytes.Repeat([]byte{' '}, 129)),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testAgentToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized request status = %d", response.StatusCode)
	}
}

func TestHTTPClientRejectsNonLoopbackOriginsAndOversizedResponses(t *testing.T) {
	for _, target := range []string{
		"https://127.0.0.1:7375",
		"http://example.com:7375",
		"http://user@127.0.0.1:7375",
		"http://127.0.0.1:7375/path",
		"http://127.0.0.1",
	} {
		if _, err := agentapi.NewHTTPClient(target, testAgentToken); !errors.Is(err, agentapi.ErrInvalid) {
			t.Fatalf("NewHTTPClient(%q) error = %v", target, err)
		}
	}
	if _, err := agentapi.NewHTTPClient("http://127.0.0.1:7375", "short"); !errors.Is(err, agentapi.ErrInvalid) {
		t.Fatalf("short token error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(bytes.Repeat([]byte{' '}, (8<<20)+1))
	}))
	defer server.Close()
	client, err := agentapi.NewHTTPClient(server.URL, testAgentToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Info(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "response is too large") {
		t.Fatalf("oversized response error = %v", err)
	}
}

package managementapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
)

func TestHTTPHandlerProtectsAndReturnsPersonaSnapshot(t *testing.T) {
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
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/management/v1/personas", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/management/v1/personas", nil)
	request.Header.Set("Authorization", "Bearer test-management-token")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s", authorized.Code, authorized.Body.String())
	}
}

func TestHTTPHandlerStartsLongGoalThroughTaskManager(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeTaskManager{}
	service, err := New(personas, memory, manager)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/management/v1/tasks/start", strings.NewReader(`{
		"task_id":"task.http-start",
		"host_id":"host.one",
		"world_id":"world.one",
		"actor_id":"actor.one",
		"goal":"Prepare supplies and complete the current world objective."
	}`))
	request.Header.Set("Authorization", "Bearer test-management-token")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || manager.task.PlanningMode != "required" ||
		manager.task.ControllerID != "controller.rin-console" {
		t.Fatalf("status=%d body=%s task=%#v", response.Code, response.Body.String(), manager.task)
	}
}

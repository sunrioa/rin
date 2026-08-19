package managementapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
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

func TestDecodeJSONRejectsBodiesLargerThanLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", io.LimitReader(
		strings.NewReader(`{"value":"`+strings.Repeat("x", int(maxRequestBytes))+`"}`),
		maxRequestBytes+128,
	))
	var target map[string]string
	if err := decodeJSON(request, &target); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("decodeJSON error = %v", err)
	}
}

func TestHTTPHandlerExposesRuntimeAndOperationHistory(t *testing.T) {
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
	control := controlplane.New(controlplane.Options{})
	principal := host.Principal{
		ID: "rin.console",
		GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl,
			controlplane.ScopeHostAdmin, controlplane.ScopeOperationCancel,
		},
	}
	if err := service.ConfigureControl(control, principal); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		path   string
		body   string
		want   string
	}{
		{http.MethodGet, "/management/v1/runtime", "", `"worlds":[]`},
		{http.MethodPost, "/management/v1/operations/list", `{}`, `"operations":[]`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer test-management-token")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
			t.Fatalf("%s: status=%d body=%s", test.path, response.Code, response.Body.String())
		}
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

func TestHTTPHandlerListsSkillsWithoutExternalSkillScope(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	learned, err := cognition.OpenDirectorySkillProvider(t.TempDir(), "learned", true)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := cognition.NewSkillCatalog(learned)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureSkills(catalog, learned); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "/management/v1/skills/list", strings.NewReader(`{"limit":128}`),
	)
	request.Header.Set("Authorization", "Bearer test-management-token")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"skills"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerExposesMetadataOnlyDiagnostics(t *testing.T) {
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
	if err := service.ConfigureDiagnostics(func(context.Context) (DiagnosticsSnapshot, error) {
		return DiagnosticsSnapshot{
			CheckedAt: 123,
			Model: ModelConfigMetadata{
				Enabled: true, Provider: "openai-compatible", Endpoint: "https://model.example/v1",
				Model: "test-model", CredentialConfigured: true,
			},
			Policy:      PolicyConfigMetadata{Revision: 4, Profile: "survival", RuleCount: 2},
			Permissions: PermissionMetadata{PrincipalID: "player.one", ControlScopes: []string{"actor.read"}},
			MCP:         MCPConfigMetadata{Commands: []MCPCommand{{ID: "install", Command: "rin mcp install"}}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/management/v1/diagnostics", nil)
	request.Header.Set("Authorization", "Bearer test-management-token")
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"credential_configured":true`) ||
		!strings.Contains(body, `"profile":"survival"`) || !strings.Contains(body, `rin mcp install`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	if strings.Contains(body, "sk-secret") || strings.Contains(body, "api_key") {
		t.Fatalf("diagnostics exposed secret material: %s", body)
	}
}

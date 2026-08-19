package managementapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/cognition"
)

type fakeAgentConfigEditor struct {
	snapshot AgentConfigSnapshot
	saved    AgentConfigSaveRequest
	saveErr  error
}

func (editor *fakeAgentConfigEditor) AgentConfig(context.Context) (AgentConfigSnapshot, error) {
	return editor.snapshot, nil
}

func (editor *fakeAgentConfigEditor) SaveAgentConfig(_ context.Context, request AgentConfigSaveRequest) (AgentConfigSaveResponse, error) {
	if editor.saveErr != nil {
		return AgentConfigSaveResponse{}, editor.saveErr
	}
	editor.saved = request
	editor.snapshot.Model = request.Model
	editor.snapshot.Configured = true
	editor.snapshot.CredentialConfigured = request.APIKey != nil && *request.APIKey != ""
	return AgentConfigSaveResponse{AgentConfigSnapshot: editor.snapshot, RequiresRestart: true}, nil
}

func TestHTTPHandlerMapsInvalidAgentConfigToBadRequest(t *testing.T) {
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
	if err := service.ConfigureAgentConfig(&fakeAgentConfigEditor{saveErr: ErrInvalidAgentConfig}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/management/v1/agent/config", strings.NewReader(`{"model":{}}`))
	request.Header.Set("Authorization", "Bearer test-management-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
	}
}

func TestHTTPHandlerAgentConfigNeverReturnsCredential(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	editor := &fakeAgentConfigEditor{snapshot: AgentConfigSnapshot{
		Configured: true,
		Model: agentdaemon.ModelConfig{
			Provider: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1", Model: "test-model",
		},
		CredentialConfigured: true,
	}}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureAgentConfig(editor); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/management/v1/agent/config", nil)
	request.Header.Set("Authorization", "Bearer test-management-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"credential_configured":true`) ||
		strings.Contains(body, "api_key") || strings.Contains(body, "secret") {
		t.Fatalf("unsafe GET response: status=%d body=%s", response.Code, body)
	}
	request = httptest.NewRequest(http.MethodPut, "/management/v1/agent/config", strings.NewReader(`{
		"model":{"provider":"openai-compatible","base_url":"http://127.0.0.1:1/v1","model":"test-model"},
		"api_key":"secret-only-in-request"
	}`))
	request.Header.Set("Authorization", "Bearer test-management-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body = response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"requires_restart":true`) ||
		strings.Contains(body, "secret-only-in-request") || strings.Contains(body, `"api_key"`) {
		t.Fatalf("unsafe PUT response: status=%d body=%s", response.Code, body)
	}
	if editor.saved.APIKey == nil || *editor.saved.APIKey != "secret-only-in-request" {
		t.Fatal("API key was not passed only to the editor")
	}
}

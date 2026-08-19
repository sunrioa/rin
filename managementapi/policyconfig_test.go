package managementapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/policy"
)

type fakePolicyConfigEditor struct {
	snapshot PolicyConfigSnapshot
	saveErr  error
}

func (editor *fakePolicyConfigEditor) PolicyConfig(context.Context) (PolicyConfigSnapshot, error) {
	return editor.snapshot, nil
}

func (editor *fakePolicyConfigEditor) SavePolicyConfig(
	_ context.Context,
	request PolicyConfigSaveRequest,
) (PolicyConfigSnapshot, error) {
	if editor.saveErr != nil {
		return PolicyConfigSnapshot{}, editor.saveErr
	}
	editor.snapshot.Config = request.Config
	editor.snapshot.Config.Revision = request.ExpectedRevision + 1
	editor.snapshot.Configured = true
	return editor.snapshot, nil
}

func TestHTTPHandlerPolicyConfigReadAndWrite(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	editor := &fakePolicyConfigEditor{snapshot: PolicyConfigSnapshot{Config: policy.Config{
		Revision: 1, Profile: policy.ProfileGuarded,
	}}}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigurePolicyConfig(editor); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/management/v1/policy/config", nil)
	request.Header.Set("Authorization", "Bearer test-management-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"profile":"guarded"`) {
		t.Fatalf("GET policy response: status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/management/v1/policy/config", strings.NewReader(`{
		"expected_revision":1,
		"config":{"revision":1,"profile":"open","confirmation_ttl":{},"confirmation_scopes":[]}
	}`))
	request.Header.Set("Authorization", "Bearer test-management-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"profile":"open"`) {
		t.Fatalf("PUT policy response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerMapsPolicyConfigErrors(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want int
	}{
		"invalid":  {ErrInvalidPolicyConfig, http.StatusBadRequest},
		"conflict": {ErrPolicyConfigConflict, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
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
			if err := service.ConfigurePolicyConfig(&fakePolicyConfigEditor{saveErr: testCase.err}); err != nil {
				t.Fatal(err)
			}
			handler, err := NewHTTPHandler(service, HTTPOptions{Token: "test-management-token"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPut, "/management/v1/policy/config", strings.NewReader(`{"config":{}}`))
			request.Header.Set("Authorization", "Bearer test-management-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status = %d, want %d", response.Code, testCase.want)
			}
		})
	}
}

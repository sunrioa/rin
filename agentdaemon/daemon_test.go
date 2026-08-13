package agentdaemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/provider"
)

const testAgentToken = "0123456789abcdef0123456789abcdef"

func TestOpenExposesOnlyConfiguredTaskPrincipal(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	daemon, err := Open(Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken,
		GenerationProvider: inertGenerationProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()
	request := httptest.NewRequest(http.MethodGet, "/agent/v1/info", nil)
	request.Header.Set("Authorization", "Bearer "+testAgentToken)
	response := httptest.NewRecorder()
	daemon.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var info agentapi.ClientInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Principal.ID != "rin.agent-client" || len(info.Principal.GrantedScopes) != 3 {
		t.Fatalf("Agent principal = %#v", info.Principal)
	}
	for _, scope := range info.Principal.GrantedScopes {
		if scope == controlplane.ScopeHostAdmin || scope == controlplane.ScopeActorExecute {
			t.Fatalf("authority scope leaked through Agent API: %q", scope)
		}
	}
}

func TestOpenAuthenticationModesDoNotProbeNetwork(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	if _, err := Open(Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken,
	}); err == nil {
		t.Fatal("bearer authentication without RIN_AGENT_API_KEY was accepted")
	}

	daemon, err := Open(Options{
		Config: testConfig(AuthenticationNone), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken,
	})
	if err != nil {
		t.Fatalf("no-auth local provider startup made a network-dependent failure: %v", err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(Options{
		Config: testConfig(AuthenticationNone), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken, APIKey: "unexpected-key",
	}); err == nil {
		t.Fatal("unused API key was accepted in no-auth mode")
	}
}

func TestCloseReleasesPersistentStoresForRestart(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	dataDirectory := t.TempDir()
	options := Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: dataDirectory,
		Control: control, HTTPToken: testAgentToken,
		GenerationProvider: inertGenerationProvider{},
	}
	first, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(options); err == nil {
		t.Fatal("a second daemon acquired the same persistent stores")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tasks.json", "memory.db"} {
		if info, err := os.Stat(filepath.Join(dataDirectory, "agent", name)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("persistent %s was not created: %v", name, err)
		}
	}
	second, err := Open(options)
	if err != nil {
		t.Fatalf("restart after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerFailureDoesNotLeakStoreLocks(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	dataDirectory := t.TempDir()
	options := Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: dataDirectory,
		Control: control, HTTPToken: "short",
		GenerationProvider: inertGenerationProvider{},
	}
	if _, err := Open(options); err == nil {
		t.Fatal("short Agent token was accepted")
	}
	options.HTTPToken = testAgentToken
	daemon, err := Open(options)
	if err != nil {
		t.Fatalf("failed startup leaked a store lock: %v", err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
}

type inertGenerationProvider struct{}

func (inertGenerationProvider) Complete(
	context.Context,
	provider.CompletionRequest,
) (provider.CompletionResponse, error) {
	return provider.CompletionResponse{}, context.Canceled
}

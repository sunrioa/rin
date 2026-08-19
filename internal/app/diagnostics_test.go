package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/managementapi"
	"github.com/sunrioa/rin/policy"
)

func TestCollectDiagnosticsRedactsEndpointCredentials(t *testing.T) {
	engine, err := policy.New(policy.Config{
		Revision: 1, Profile: policy.ProfileGuarded,
		ConfirmationTTL:    policy.ConfirmationDurations{Event: 1, Step: 1, Realtime: 1},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	config := configuration{
		address: "127.0.0.1:7375", agentConfig: "/private/agent.json",
		agentAPIKey: "sk-secret", agentEmbeddingAPIKey: "embedding-secret",
		principal: managementPrincipal(testPrincipal()),
	}
	agentConfig := agentdaemon.Config{
		Model: agentdaemon.ModelConfig{
			Provider: "openai-compatible", BaseURL: "https://user:pass@example.test/v1?key=secret",
			Model: "test-model", Authentication: agentdaemon.AuthenticationBearerEnv,
		},
		Memory: agentdaemon.MemoryConfig{SemanticEmbedding: agentdaemon.SemanticEmbeddingConfig{
			Enabled: true, BaseURL: "https://embed:secret@example.test/v1?token=secret", Model: "embed-model",
		}},
	}
	result := collectDiagnostics(context.Background(), diagnosticsDependencies{
		Control: controlplane.New(controlplane.Options{}), Policy: engine,
		Config: config, AgentConfig: agentConfig,
	})
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	for _, secret := range []string{"sk-secret", "embedding-secret", "user:pass", "?key=secret", "?token=secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, body)
		}
	}
	if result.Model.Endpoint != "https://example.test/v1" || result.Memory.SemanticEndpoint != "https://example.test/v1" {
		t.Fatalf("redacted endpoints = %#v %#v", result.Model.Endpoint, result.Memory.SemanticEndpoint)
	}
	if !result.Model.CredentialConfigured || !result.Memory.SemanticCredentialConfigured {
		t.Fatal("credential presence was not retained as metadata")
	}
	if result.Policy.Profile != string(policy.ProfileGuarded) || result.Policy.Revision != 1 {
		t.Fatalf("policy metadata = %#v", result.Policy)
	}
}

func TestCollectDiagnosticsReportsControlAndPermissions(t *testing.T) {
	principal := testPrincipal()
	result := collectDiagnostics(context.Background(), diagnosticsDependencies{
		Control: controlplane.New(controlplane.Options{}),
		Config:  configuration{address: "127.0.0.1:7375", principal: principal},
	})
	if len(result.Connections) != 3 || result.Connections[2].ID != "control-plane" ||
		result.Connections[2].Status != managementapi.DiagnosticOK {
		t.Fatalf("connections = %#v", result.Connections)
	}
	if result.Permissions.PrincipalID != principal.ID || len(result.Permissions.ControlScopes) == 0 {
		t.Fatalf("permissions = %#v", result.Permissions)
	}
}

func testPrincipal() host.Principal {
	return host.Principal{ID: "player.one", GrantedScopes: []string{controlplane.ScopeActorRead}}
}

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/skillapi"
)

func TestParseConfigurationUsesBoundedReadPrincipal(t *testing.T) {
	config, err := parseConfiguration(
		nil,
		testEnvironment(map[string]string{
			"RIN_CONTROL_TOKEN":     "0123456789abcdef0123456789abcdef",
			"RIN_CONTROL_PRINCIPAL": "player.one",
		}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfiguration: %v", err)
	}
	if config.address != "127.0.0.1:7375" ||
		config.dataDir != "./rin-control-data" ||
		config.principal.ID != "player.one" ||
		len(config.principal.GrantedScopes) != 2 ||
		config.principal.GrantedScopes[0] != controlplane.ScopeActorRead ||
		config.principal.GrantedScopes[1] != skillapi.ScopeSkillRead {
		t.Fatalf("configuration = %#v", config)
	}
}

func TestParseConfigurationAcceptsActorControlOnlyPrincipal(t *testing.T) {
	config, err := parseConfiguration(
		nil,
		testEnvironment(map[string]string{
			"RIN_CONTROL_TOKEN":     "0123456789abcdef0123456789abcdef",
			"RIN_CONTROL_PRINCIPAL": "player.one",
			"RIN_CONTROL_SCOPES":    controlplane.ScopeActorControl,
		}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfiguration: %v", err)
	}
	if len(config.principal.GrantedScopes) != 1 ||
		config.principal.GrantedScopes[0] != controlplane.ScopeActorControl {
		t.Fatalf("configuration = %#v", config)
	}
}

func TestParseConfigurationRejectsMissingCredentials(t *testing.T) {
	_, err := parseConfiguration(
		nil,
		testEnvironment(map[string]string{
			"RIN_CONTROL_PRINCIPAL": "player.one",
		}),
		io.Discard,
	)
	if err == nil {
		t.Fatal("missing token was accepted")
	}

	_, err = parseConfiguration(
		nil,
		testEnvironment(map[string]string{
			"RIN_CONTROL_TOKEN": "0123456789abcdef0123456789abcdef",
		}),
		io.Discard,
	)
	if err == nil {
		t.Fatal("missing principal was accepted")
	}
}

func TestParseConfigurationKeepsAgentDisabledByDefault(t *testing.T) {
	config, err := parseConfiguration(
		nil,
		testEnvironment(map[string]string{
			"RIN_CONTROL_TOKEN":     "0123456789abcdef0123456789abcdef",
			"RIN_CONTROL_PRINCIPAL": "player.one",
		}),
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.agentConfig != "" || config.agentToken != "" || config.agentAPIKey != "" {
		t.Fatalf("Agent Runtime unexpectedly enabled: %#v", config)
	}
}

func TestParseConfigurationRequiresExplicitAgentConfigAndToken(t *testing.T) {
	base := map[string]string{
		"RIN_CONTROL_TOKEN":     "0123456789abcdef0123456789abcdef",
		"RIN_CONTROL_PRINCIPAL": "player.one",
	}
	for name, value := range map[string]string{
		"RIN_AGENT_TOKEN":   "0123456789abcdef0123456789abcdef",
		"RIN_AGENT_API_KEY": "provider-key",
	} {
		environment := map[string]string{}
		for key, existing := range base {
			environment[key] = existing
		}
		environment[name] = value
		if _, err := parseConfiguration(nil, testEnvironment(environment), io.Discard); err == nil {
			t.Fatalf("%s without Agent config was accepted", name)
		}
	}
	environment := map[string]string{}
	for key, value := range base {
		environment[key] = value
	}
	environment["RIN_AGENT_CONFIG"] = "/private/agent.json"
	if _, err := parseConfiguration(nil, testEnvironment(environment), io.Discard); err == nil {
		t.Fatal("Agent config without Agent token was accepted")
	}
	environment["RIN_AGENT_TOKEN"] = environment["RIN_CONTROL_TOKEN"]
	if _, err := parseConfiguration(nil, testEnvironment(environment), io.Discard); err == nil {
		t.Fatal("shared Control and Agent token was accepted")
	}
	environment["RIN_AGENT_TOKEN"] = "fedcba9876543210fedcba9876543210"
	environment["RIN_AGENT_API_KEY"] = environment["RIN_AGENT_TOKEN"]
	if _, err := parseConfiguration(nil, testEnvironment(environment), io.Discard); err == nil {
		t.Fatal("Agent API key reused as a daemon token was accepted")
	}
	environment["RIN_AGENT_API_KEY"] = "provider-key"
	config, err := parseConfiguration(nil, testEnvironment(environment), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if config.agentConfig != "/private/agent.json" ||
		config.agentToken != environment["RIN_AGENT_TOKEN"] ||
		config.agentAPIKey != "provider-key" {
		t.Fatalf("Agent configuration was not retained: %#v", config)
	}
}

func TestComposeHandlersKeepsRoutesSeparate(t *testing.T) {
	control := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Handler", "control")
		response.WriteHeader(http.StatusNoContent)
	})
	agent := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Handler", "agent")
		response.WriteHeader(http.StatusAccepted)
	})
	skills := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Handler", "skills")
		response.WriteHeader(http.StatusOK)
	})
	plans := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Handler", "plans")
		response.WriteHeader(http.StatusCreated)
	})
	signals := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Handler", "signals")
		response.WriteHeader(http.StatusPartialContent)
	})
	handler := composeHandlers(control, skills, agent, plans, signals)
	for _, test := range []struct {
		path, expected string
		status         int
	}{
		{path: "/control/v2/info", expected: "control", status: http.StatusNoContent},
		{path: "/skills/v1/list", expected: "skills", status: http.StatusOK},
		{path: "/plans/v1/get", expected: "plans", status: http.StatusCreated},
		{path: "/signals/v1/list", expected: "signals", status: http.StatusPartialContent},
		{path: "/agent/v1/info", expected: "agent", status: http.StatusAccepted},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.status || response.Header().Get("X-Handler") != test.expected {
			t.Fatalf("%s routed to %q with status %d", test.path, response.Header().Get("X-Handler"), response.Code)
		}
	}
}

func TestComposedHandlersKeepTokensAndPrincipalsIsolated(t *testing.T) {
	const controlToken = "control-token-0123456789abcdef0123456789"
	const agentToken = "agent-token-0123456789abcdef012345678901"
	service := controlplane.New(controlplane.Options{})
	defer service.Close()
	principal := host.Principal{
		ID: "player.one", GrantedScopes: []string{controlplane.ScopeActorRead},
	}
	controlHandler, err := controlplane.NewHTTPHandler(service, controlplane.HTTPOptions{
		Token: controlToken, ClientPrincipal: &principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentdaemon.Open(agentdaemon.Options{
		Config: agentdaemon.Config{
			ContractVersion: agentdaemon.ConfigVersion,
			Model: agentdaemon.ModelConfig{
				BaseURL: "http://127.0.0.1:1/v1", Model: "test-model",
				Authentication: agentdaemon.AuthenticationBearerEnv,
			},
			Personas: []cognition.PersonaProfile{{
				PersonaID: "companion", Version: "v1", Identity: "A careful companion.",
			}},
			PersonaBindings: []cognition.PersonaBinding{{
				ActorID: "actor.one", PersonaID: "companion", Version: "v1",
			}},
		},
		DataDir: t.TempDir(), Control: service, HTTPToken: agentToken,
		GenerationProvider: commandTestGenerationProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	skillHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	handler := composeHandlers(controlHandler, skillHandler, agent.Handler())
	for _, test := range []struct {
		path, token string
		status      int
	}{
		{path: "/agent/v1/info", token: controlToken, status: http.StatusUnauthorized},
		{path: "/control/v2/info", token: agentToken, status: http.StatusUnauthorized},
		{path: "/agent/v1/info", token: agentToken, status: http.StatusOK},
		{path: "/control/v2/info", token: controlToken, status: http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s with selected token returned %d, want %d", test.path, response.Code, test.status)
		}
	}
}

func TestParseConfigurationRejectsRemoteAddressAndMalformedScope(t *testing.T) {
	environment := testEnvironment(map[string]string{
		"RIN_CONTROL_TOKEN":     "0123456789abcdef0123456789abcdef",
		"RIN_CONTROL_PRINCIPAL": "player.one",
	})
	if _, err := parseConfiguration(
		[]string{"-control-addr", "0.0.0.0:7375"},
		environment,
		io.Discard,
	); err == nil {
		t.Fatal("remote address was accepted")
	}
	if _, err := parseConfiguration(
		[]string{"-scopes", "actor.read,bad scope"},
		environment,
		io.Discard,
	); err == nil {
		t.Fatal("malformed scope was accepted")
	}
}

func TestParseConfigurationAllowsGameSpecificCapabilityScopes(t *testing.T) {
	config, err := parseConfiguration(
		[]string{"-scopes", "actor.execute,minecraft.inventory"},
		testEnvironment(map[string]string{
			"RIN_CONTROL_TOKEN":     "0123456789abcdef0123456789abcdef",
			"RIN_CONTROL_PRINCIPAL": "player.one",
		}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfiguration: %v", err)
	}
	if len(config.principal.GrantedScopes) != 2 ||
		config.principal.GrantedScopes[1] != "minecraft.inventory" {
		t.Fatalf("scopes = %#v", config.principal.GrantedScopes)
	}
}

func TestValidateLoopbackAddress(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:0",
		"[::1]:7375",
		"localhost:7375",
	} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
}

func TestLoadPolicyEngineDefaultsToFailClosedCatalog(t *testing.T) {
	engine, err := loadPolicyEngine("")
	if err != nil {
		t.Fatalf("loadPolicyEngine: %v", err)
	}
	config := engine.Config()
	if config.Profile != policy.ProfileGuarded ||
		len(config.KnownEffectKinds) != 0 || len(config.KnownScopes) != 0 ||
		config.ConfirmationTTL != (policy.ConfirmationDurations{
			Event: 16, Step: 600, Realtime: 30_000,
		}) {
		t.Fatalf("default policy = %#v", config)
	}
}

func TestLoadPolicyEngineUsesStrictConfiguredPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{
  "revision": 7,
  "profile": "open",
  "known_effect_kinds": ["world.position"],
  "known_scopes": ["world.public"],
  "confirmation_ttl": {"step": 20},
  "confirmation_scopes": ["rin.policy.confirm"]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := loadPolicyEngine(path)
	if err != nil {
		t.Fatalf("loadPolicyEngine: %v", err)
	}
	if config := engine.Config(); config.Revision != 7 ||
		config.Profile != policy.ProfileOpen ||
		config.ConfirmationTTL != (policy.ConfirmationDurations{Step: 20}) {
		t.Fatalf("configured policy = %#v", config)
	}
	if err := os.WriteFile(path, []byte(`{
  "revision": 7,
  "profile": "open",
  "confirmation_ttl": {"clock": "step", "value": 20},
  "confirmation_scopes": ["rin.policy.confirm"]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPolicyEngine(path); err == nil {
		t.Fatal("legacy single-clock policy shape was accepted")
	}
	if err := os.WriteFile(path, []byte(`{
  "revision": 7,
  "profile": "open",
  "unknown": true,
  "confirmation_ttl": {"step": 20},
  "confirmation_scopes": ["rin.policy.confirm"]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPolicyEngine(path); err == nil {
		t.Fatal("policy with unknown field was accepted")
	}
}

func testEnvironment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}

type commandTestGenerationProvider struct{}

func (commandTestGenerationProvider) Complete(
	context.Context,
	provider.CompletionRequest,
) (provider.CompletionResponse, error) {
	return provider.CompletionResponse{}, context.Canceled
}

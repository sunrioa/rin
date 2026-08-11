package agentdaemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/privatefile"
)

func TestLoadConfigAppliesTaskOnlyDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := privatefile.WriteJSON(path, testConfig(AuthenticationNone)); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ClientPrincipal.ID != "rin.agent-client" ||
		config.RuntimePrincipal != "rin.internal" ||
		len(config.ClientPrincipal.GrantedScopes) != 3 ||
		config.ClientPrincipal.GrantedScopes[0] != agentapi.ScopeTaskRead {
		t.Fatalf("defaults = %#v", config)
	}
	if config.Model.Provider != ProviderOpenAICompatible ||
		config.Model.ResponseFormat != "json_schema" {
		t.Fatalf("model defaults = %#v", config.Model)
	}
}

func TestLoadConfigRejectsCredentialsAndAmbiguousJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	payload, err := json.Marshal(testConfig(AuthenticationNone))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	document["model"].(map[string]any)["api_key"] = "forbidden-config-value"
	payload, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("configuration containing api_key was accepted")
	}

	duplicate := []byte(`{"contract_version":"rin.agent.config/v1","contract_version":"rin.agent.config/v1"}`)
	if err := os.WriteFile(path, duplicate, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("configuration containing duplicate fields was accepted")
	}
}

func TestNormalizeConfigRejectsControlPlaneAuthority(t *testing.T) {
	for _, scope := range []string{"host.admin", "actor.read", "minecraft.command"} {
		config := testConfig(AuthenticationNone)
		config.ClientPrincipal = host.Principal{
			ID: "rin.agent-client", GrantedScopes: []string{scope},
		}
		if _, err := normalizeConfig(config); err == nil {
			t.Fatalf("non-task scope %q was accepted", scope)
		}
	}
}

func TestNormalizeConfigRejectsRemotePlaintextModelTransport(t *testing.T) {
	config := testConfig(AuthenticationBearerEnv)
	config.Model.BaseURL = "http://models.example.test/v1"
	if _, err := normalizeConfig(config); err == nil {
		t.Fatal("remote plaintext model transport was accepted for bearer authentication")
	}
	config.Model.Authentication = AuthenticationNone
	if _, err := normalizeConfig(config); err == nil {
		t.Fatal("remote plaintext model transport was accepted without authentication")
	}
	config.Model.BaseURL = "http://localhost:11434/v1"
	if _, err := normalizeConfig(config); err != nil {
		t.Fatalf("loopback development transport was rejected: %v", err)
	}
}

func testConfig(authentication string) Config {
	return Config{
		ContractVersion: ConfigVersion,
		Model: ModelConfig{
			BaseURL: "http://127.0.0.1:1/v1", Model: "test-model",
			Authentication: authentication,
		},
		Personas: []cognition.PersonaProfile{{
			PersonaID: "companion", Version: "v1", Identity: "A grounded companion.",
		}},
		PersonaBindings: []cognition.PersonaBinding{{
			ActorID: "actor.one", PersonaID: "companion", Version: "v1",
		}},
	}
}

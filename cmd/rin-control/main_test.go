package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/policy"
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
		len(config.principal.GrantedScopes) != 1 ||
		config.principal.GrantedScopes[0] != controlplane.ScopeActorRead {
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
		len(config.KnownEffectKinds) != 0 || len(config.KnownScopes) != 0 {
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
  "confirmation_ttl": {"clock": "step", "value": 20},
  "confirmation_scopes": ["rin.policy.confirm"]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := loadPolicyEngine(path)
	if err != nil {
		t.Fatalf("loadPolicyEngine: %v", err)
	}
	if config := engine.Config(); config.Revision != 7 ||
		config.Profile != policy.ProfileOpen {
		t.Fatalf("configured policy = %#v", config)
	}
	if err := os.WriteFile(path, []byte(`{
  "revision": 7,
  "profile": "open",
  "unknown": true,
  "confirmation_ttl": {"clock": "step", "value": 20},
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

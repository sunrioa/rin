package main

import (
	"io"
	"testing"

	"github.com/sunrioa/rin/controlplane"
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

func testEnvironment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}

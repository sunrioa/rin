package main

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/mcpconfig"
)

const testControlToken = "0123456789abcdef0123456789abcdef"

func TestParseConfigurationUsesControlDaemon(t *testing.T) {
	config, err := parseConfiguration(
		nil,
		testEnvironment(map[string]string{
			"RIN_CONTROL_TOKEN": testControlToken,
		}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfiguration: %v", err)
	}
	if config.controlURL != "http://127.0.0.1:7375" {
		t.Fatalf("configuration = %#v", config)
	}
}

func TestParseConfigurationRejectsMissingCredentials(t *testing.T) {
	_, err := parseConfiguration(
		nil,
		testEnvironment(map[string]string{}),
		io.Discard,
	)
	if err == nil {
		t.Fatal("missing token was accepted")
	}
}

func TestParseConfigurationRejectsRemoteControlURL(t *testing.T) {
	environment := testEnvironment(map[string]string{
		"RIN_CONTROL_TOKEN": testControlToken,
	})
	if _, err := parseConfiguration(
		[]string{"-control-url", "http://0.0.0.0:7375"},
		environment,
		io.Discard,
	); err == nil {
		t.Fatal("remote Control URL was accepted")
	}
	if _, err := parseConfiguration(
		[]string{"-conformance-addr", "0.0.0.0:7380"},
		environment,
		io.Discard,
	); err == nil {
		t.Fatal("remote conformance address was accepted")
	}
}

func TestParseConfigurationLoadsPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-client.json")
	if err := mcpconfig.Write(
		path,
		mcpconfig.New("http://127.0.0.1:7385", testControlToken),
	); err != nil {
		t.Fatal(err)
	}
	config, err := parseConfiguration(
		[]string{"-config", path},
		testEnvironment(map[string]string{}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfiguration: %v", err)
	}
	if config.controlURL != "http://127.0.0.1:7385" ||
		config.token != testControlToken {
		t.Fatalf("configuration = %#v", config)
	}
}

func TestParseConfigurationPrivateFileOverridesEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-client.json")
	if err := mcpconfig.Write(
		path,
		mcpconfig.New("http://127.0.0.1:7385", testControlToken),
	); err != nil {
		t.Fatal(err)
	}
	overrideToken := "abcdef0123456789abcdef0123456789"
	config, err := parseConfiguration(
		[]string{"-config", path},
		testEnvironment(map[string]string{
			"RIN_CONTROL_URL":   "http://127.0.0.1:7395",
			"RIN_CONTROL_TOKEN": overrideToken,
		}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfiguration: %v", err)
	}
	if config.controlURL != "http://127.0.0.1:7385" ||
		config.token != testControlToken {
		t.Fatalf("configuration = %#v", config)
	}
	config, err = parseConfiguration(
		[]string{
			"-config", path,
			"-control-url", "http://127.0.0.1:7396",
		},
		testEnvironment(map[string]string{}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfiguration explicit URL: %v", err)
	}
	if config.controlURL != "http://127.0.0.1:7396" ||
		config.token != testControlToken {
		t.Fatalf("explicit URL configuration = %#v", config)
	}
}

func TestConformanceHTTPRequiresReadOnlyPrincipal(t *testing.T) {
	if !readOnlyConformancePrincipal(host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{controlplane.ScopeActorRead},
	}) {
		t.Fatal("read-only principal was rejected")
	}
	if readOnlyConformancePrincipal(host.Principal{
		ID: "player.one",
		GrantedScopes: []string{
			controlplane.ScopeActorRead,
			controlplane.ScopeActorExecute,
		},
	}) {
		t.Fatal("mutating principal was accepted")
	}
}

func testEnvironment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}

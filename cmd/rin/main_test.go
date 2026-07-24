package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestVersionProjection(t *testing.T) {
	if version != protocol.ContractReleaseVersion {
		t.Fatalf("CLI version = %q, contract version = %q", version, protocol.ContractReleaseVersion)
	}
}

func TestValidateListenAddress(t *testing.T) {
	tests := []struct {
		name        string
		address     string
		allowRemote bool
		token       string
		wantError   bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1:7374"},
		{name: "IPv6 loopback", address: "[::1]:7374"},
		{name: "localhost", address: "localhost:7374"},
		{name: "remote denied", address: "0.0.0.0:7374", wantError: true},
		{name: "remote needs token", address: "0.0.0.0:7374", allowRemote: true, wantError: true},
		{name: "remote explicit", address: "0.0.0.0:7374", allowRemote: true, token: "token"},
		{name: "invalid", address: "7374", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateListenAddress(test.address, test.allowRemote, test.token)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestValidateModelEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		insecure  bool
		wantError bool
	}{
		{name: "https", url: "https://models.example/v1"},
		{name: "local IPv4", url: "http://127.0.0.1:8080/v1"},
		{name: "local IPv6", url: "http://[::1]:8080/v1"},
		{name: "remote HTTP denied", url: "http://models.example/v1", wantError: true},
		{name: "remote HTTP explicit", url: "http://models.example/v1", insecure: true},
		{name: "userinfo denied", url: "https://user@models.example/v1", wantError: true},
		{name: "file denied", url: "file:///tmp/model", wantError: true},
		{name: "query denied", url: "https://models.example/v1?redirect=1", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateModelEndpoint(test.url, test.insecure)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestBuildPolicyModes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("RIN_POLICY", "deterministic")
	selected, mode, err := buildPolicy(logger)
	if err != nil || selected == nil || mode != "deterministic" {
		t.Fatalf("deterministic policy: mode=%s err=%v", mode, err)
	}

	t.Setenv("RIN_POLICY", "model")
	t.Setenv("RIN_MODEL_BASE_URL", "")
	t.Setenv("RIN_MODEL", "")
	if _, _, err := buildPolicy(logger); err == nil {
		t.Fatal("missing model configuration should fail")
	}
	t.Setenv("RIN_MODEL_BASE_URL", "http://127.0.0.1:9999/v1")
	t.Setenv("RIN_MODEL", "fixture-model")
	t.Setenv("RIN_MODEL_API_KEY", "")
	selected, mode, err = buildPolicy(logger)
	if err != nil || selected == nil || mode != "model-with-fallback" {
		t.Fatalf("local model policy: mode=%s err=%v", mode, err)
	}

	t.Setenv("RIN_MODEL_BASE_URL", "https://models.example/v1")
	if _, _, err := buildPolicy(logger); err == nil {
		t.Fatal("remote model without API key should fail")
	}
}

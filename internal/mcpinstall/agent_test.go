package mcpinstall

import (
	"bytes"
	"testing"
)

func TestLimitedCommandOutputIsBounded(t *testing.T) {
	output := &limitedCommandOutput{}
	payload := bytes.Repeat([]byte("x"), maxAgentCommandOutput*2)
	written, err := output.Write(payload)
	if err != nil {
		t.Fatal(err)
	}
	if written != len(payload) {
		t.Fatalf("Write reported %d bytes, want %d", written, len(payload))
	}
	if len(output.bytes()) != maxAgentCommandOutput {
		t.Fatalf("retained output = %d bytes", len(output.bytes()))
	}
}

func TestRegistrationMissingRequiresExplicitMCPMessage(t *testing.T) {
	if !registrationIsMissing([]byte("No MCP server named 'rin' found")) {
		t.Fatal("known missing registration was rejected")
	}
	for _, message := range []string{
		"permission denied",
		"configuration file not found",
		"No MCP server named 'other' found",
	} {
		if registrationIsMissing([]byte(message)) {
			t.Fatalf("unexpected missing classification for %q", message)
		}
	}
}

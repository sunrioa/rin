package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sunrioa/rin/internal/hostscaffold"
)

func TestHostProjectCommands(t *testing.T) {
	root := generateCommandHost(t)
	inputSchema := filepath.Join(t.TempDir(), "movement-input.json")
	if err := os.WriteFile(inputSchema, []byte(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target_id":{"type":"string"}},"required":["target_id"],"additionalProperties":false}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runConformance([]string{"host", "-path", root}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "conforms to rin.protocol/v2") {
		t.Fatalf("unexpected conformance output: %s", output.String())
	}

	output.Reset()
	if err := runAdd([]string{
		"skill", "-path", root, "-id", "movement.follow",
		"-input-schema", inputSchema,
		"-execution", "long-running", "-effect", "world-mutation",
	}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "movement.follow@1.0.0.json") {
		t.Fatalf("unexpected add output: %s", output.String())
	}
	if _, err := os.Stat(filepath.Join(
		root, "capabilities", "movement.follow@1.0.0.json",
	)); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if err := runDoctor([]string{"host", "-path", root}, &output); err != nil {
		t.Fatal(err)
	}
	pythonExecutable := "python3"
	if runtime.GOOS == "windows" {
		pythonExecutable = "python"
	}
	for _, fragment := range []string{
		"conformance=pass", "runtime=python", "executable=" + pythonExecutable,
		"status=",
	} {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("doctor output is missing %q: %s", fragment, output.String())
		}
	}
}

func TestInitModLegacyCommandIsRemoved(t *testing.T) {
	var output bytes.Buffer
	err := runInit([]string{"mod"}, &output)
	if err == nil || !strings.Contains(err.Error(), `unsupported init resource "mod"`) {
		t.Fatalf("error = %v, want removed legacy command", err)
	}
}

func generateCommandHost(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	result, err := hostscaffold.GenerateAt(parent, hostscaffold.Options{
		Host: hostscaffold.HostCustom, Runtime: "python",
		ID: "command_host", Output: "command-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Root
}

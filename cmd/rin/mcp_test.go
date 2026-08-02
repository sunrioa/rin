package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/sunrioa/rin/internal/mcpinstall"
)

func TestParseAgentListAliasesAndDuplicates(t *testing.T) {
	agents, err := parseAgentList("claude-code,codex,claude,open-claw")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(agents, []mcpinstall.AgentID{
		mcpinstall.AgentClaude,
		mcpinstall.AgentCodex,
		mcpinstall.AgentOpenClaw,
	}) {
		t.Fatalf("agents = %v", agents)
	}
}

func TestPromptForAgentsSupportsNumbersNamesAndDefault(t *testing.T) {
	statuses := []mcpinstall.AgentStatus{
		{ID: mcpinstall.AgentCodex, Available: true},
		{ID: mcpinstall.AgentClaude, Available: false},
		{ID: mcpinstall.AgentOpenClaw, Available: true},
	}
	selected, err := promptForAgents(
		strings.NewReader("2,codex\n"),
		&bytes.Buffer{},
		statuses,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(selected, []mcpinstall.AgentID{
		mcpinstall.AgentOpenClaw,
		mcpinstall.AgentCodex,
	}) {
		t.Fatalf("selected = %v", selected)
	}
	selected, err = promptForAgents(
		strings.NewReader("\n"),
		&bytes.Buffer{},
		statuses,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(selected, []mcpinstall.AgentID{
		mcpinstall.AgentCodex,
		mcpinstall.AgentOpenClaw,
	}) {
		t.Fatalf("default selected = %v", selected)
	}
}

func TestPromptForAgentsRejectsEmptyNoninteractiveInput(t *testing.T) {
	_, err := promptForAgents(
		strings.NewReader(""),
		&bytes.Buffer{},
		[]mcpinstall.AgentStatus{{
			ID:        mcpinstall.AgentCodex,
			Available: true,
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "noninteractive") {
		t.Fatalf("prompt error = %v", err)
	}
}

func TestMCPHelp(t *testing.T) {
	var output bytes.Buffer
	if err := runMCP(nil, []string{"help"}, strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "rin mcp install") {
		t.Fatalf("help = %q", output.String())
	}
}

package mcpinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	agentCommandTimeout   = 30 * time.Second
	maxAgentCommandOutput = 64 << 10
)

type AgentID string

const (
	AgentCodex    AgentID = "codex"
	AgentClaude   AgentID = "claude"
	AgentOpenClaw AgentID = "openclaw"
)

var supportedAgents = []AgentID{
	AgentCodex,
	AgentClaude,
	AgentOpenClaw,
}

type CommandResult struct {
	Output   []byte
	ExitCode int
}

type Runner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, string, ...string) (CommandResult, error)
}

type OSRunner struct{}

type limitedCommandOutput struct {
	mu   sync.Mutex
	data []byte
}

func (output *limitedCommandOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := maxAgentCommandOutput - len(output.data)
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		output.data = append(output.data, data[:remaining]...)
	}
	return len(data), nil
}

func (output *limitedCommandOutput) bytes() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return append([]byte(nil), output.data...)
}

func (OSRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (OSRunner) Run(
	ctx context.Context,
	directory string,
	name string,
	arguments ...string,
) (CommandResult, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	output := &limitedCommandOutput{}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if err == nil {
		return CommandResult{Output: output.bytes()}, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return CommandResult{
			Output:   output.bytes(),
			ExitCode: exitError.ExitCode(),
		}, nil
	}
	return CommandResult{}, err
}

type agentAdapter struct {
	id          AgentID
	displayName string
	executable  string
}

func ParseAgentID(value string) (AgentID, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case string(AgentCodex):
		return AgentCodex, nil
	case string(AgentClaude), "claude-code", "claudecode":
		return AgentClaude, nil
	case string(AgentOpenClaw), "open-claw":
		return AgentOpenClaw, nil
	default:
		return "", fmt.Errorf("unsupported MCP agent %q", value)
	}
}

func AgentDisplayName(id AgentID) string {
	switch id {
	case AgentCodex:
		return "Codex"
	case AgentClaude:
		return "Claude Code"
	case AgentOpenClaw:
		return "OpenClaw"
	default:
		return string(id)
	}
}

func agentExecutableName(id AgentID) string {
	switch id {
	case AgentCodex:
		return "codex"
	case AgentClaude:
		return "claude"
	case AgentOpenClaw:
		return "openclaw"
	default:
		return ""
	}
}

func newAgentAdapter(id AgentID, executable string) (agentAdapter, error) {
	if agentExecutableName(id) == "" || executable == "" {
		return agentAdapter{}, fmt.Errorf("unsupported MCP agent %q", id)
	}
	return agentAdapter{
		id:          id,
		displayName: AgentDisplayName(id),
		executable:  executable,
	}, nil
}

func (adapter agentAdapter) registrationArguments() []string {
	switch adapter.id {
	case AgentCodex:
		return []string{"mcp", "get", serverRegistrationName, "--json"}
	case AgentClaude:
		return []string{"mcp", "get", serverRegistrationName}
	case AgentOpenClaw:
		return []string{"mcp", "show", serverRegistrationName, "--json"}
	default:
		return nil
	}
}

func (adapter agentAdapter) registered(
	ctx context.Context,
	runner Runner,
	directory string,
) (bool, error) {
	result, err := runBoundedAgentCommand(
		ctx,
		runner,
		directory,
		adapter.executable,
		adapter.registrationArguments()...,
	)
	if err != nil {
		return false, fmt.Errorf("inspect %s MCP registration: %w", adapter.displayName, err)
	}
	if result.ExitCode == 0 {
		return true, nil
	}
	if registrationIsMissing(result.Output) {
		return false, nil
	}
	return false, fmt.Errorf(
		"inspect %s MCP registration: %s",
		adapter.displayName,
		commandFailureMessage(result),
	)
}

func (adapter agentAdapter) add(
	ctx context.Context,
	runner Runner,
	directory string,
	serverPath string,
	configPath string,
) error {
	var arguments []string
	switch adapter.id {
	case AgentCodex:
		arguments = []string{
			"mcp", "add", serverRegistrationName, "--",
			serverPath, "--config", configPath,
		}
	case AgentClaude:
		arguments = []string{
			"mcp", "add", "--transport", "stdio", "--scope", "user",
			serverRegistrationName, "--", serverPath, "--config", configPath,
		}
	case AgentOpenClaw:
		definition, err := json.Marshal(struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}{
			Command: serverPath,
			Args:    []string{"--config", configPath},
		})
		if err != nil {
			return err
		}
		arguments = []string{
			"mcp", "set", serverRegistrationName, string(definition),
		}
	default:
		return fmt.Errorf("unsupported MCP agent %q", adapter.id)
	}
	return runAgentCommand(
		ctx,
		runner,
		directory,
		adapter,
		"register",
		arguments,
	)
}

func (adapter agentAdapter) remove(
	ctx context.Context,
	runner Runner,
	directory string,
) error {
	var arguments []string
	switch adapter.id {
	case AgentCodex:
		arguments = []string{"mcp", "remove", serverRegistrationName}
	case AgentClaude:
		arguments = []string{
			"mcp", "remove", "--scope", "user", serverRegistrationName,
		}
	case AgentOpenClaw:
		arguments = []string{"mcp", "unset", serverRegistrationName}
	default:
		return fmt.Errorf("unsupported MCP agent %q", adapter.id)
	}
	return runAgentCommand(
		ctx,
		runner,
		directory,
		adapter,
		"remove",
		arguments,
	)
}

func runAgentCommand(
	ctx context.Context,
	runner Runner,
	directory string,
	adapter agentAdapter,
	action string,
	arguments []string,
) error {
	result, err := runBoundedAgentCommand(
		ctx,
		runner,
		directory,
		adapter.executable,
		arguments...,
	)
	if err != nil {
		return fmt.Errorf("%s %s MCP server: %w", action, adapter.displayName, err)
	}
	if result.ExitCode == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s %s MCP server: %s",
		action,
		adapter.displayName,
		commandFailureMessage(result),
	)
}

func registrationIsMissing(output []byte) bool {
	message := strings.ToLower(strings.TrimSpace(string(output)))
	if !strings.Contains(message, "mcp server") ||
		!strings.Contains(message, serverRegistrationName) {
		return false
	}
	return strings.Contains(message, "no mcp server") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "does not exist") ||
		strings.Contains(message, "unknown mcp server")
}

func commandFailureMessage(result CommandResult) string {
	message := strings.TrimSpace(string(result.Output))
	if len(message) > 2048 {
		message = message[:2048]
	}
	if message == "" {
		message = fmt.Sprintf("exit code %d", result.ExitCode)
	}
	return message
}

func runBoundedAgentCommand(
	ctx context.Context,
	runner Runner,
	directory string,
	executable string,
	arguments ...string,
) (CommandResult, error) {
	commandContext, cancel := context.WithTimeout(ctx, agentCommandTimeout)
	defer cancel()
	result, err := runner.Run(
		commandContext,
		directory,
		executable,
		arguments...,
	)
	if commandContext.Err() != nil {
		return CommandResult{}, commandContext.Err()
	}
	return result, err
}

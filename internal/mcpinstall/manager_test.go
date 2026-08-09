package mcpinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const managerTestToken = "0123456789abcdef0123456789abcdef"

type fakeCommand struct {
	name      string
	arguments []string
}

type fakeRunner struct {
	paths      map[string]string
	registered map[AgentID]bool
	commands   []fakeCommand
	failAction string
	failAgent  AgentID
}

func newFakeRunner(ids ...AgentID) *fakeRunner {
	runner := &fakeRunner{
		paths:      make(map[string]string),
		registered: make(map[AgentID]bool),
	}
	for _, id := range ids {
		runner.paths[agentExecutableName(id)] = "/tools/" + agentExecutableName(id)
	}
	return runner
}

func (runner *fakeRunner) LookPath(name string) (string, error) {
	path, exists := runner.paths[name]
	if !exists {
		return "", os.ErrNotExist
	}
	return path, nil
}

func (runner *fakeRunner) Run(
	_ context.Context,
	_ string,
	name string,
	arguments ...string,
) (CommandResult, error) {
	runner.commands = append(runner.commands, fakeCommand{
		name:      name,
		arguments: append([]string(nil), arguments...),
	})
	id, err := fakeAgentID(name)
	if err != nil {
		return CommandResult{}, err
	}
	action := fakeAction(id, arguments)
	if action == runner.failAction && id == runner.failAgent {
		return CommandResult{Output: []byte("simulated failure"), ExitCode: 2}, nil
	}
	switch action {
	case "get":
		if runner.registered[id] {
			return CommandResult{}, nil
		}
		return CommandResult{
			Output:   []byte("No MCP server named 'rin' found"),
			ExitCode: 1,
		}, nil
	case "add":
		runner.registered[id] = true
		return CommandResult{}, nil
	case "remove":
		runner.registered[id] = false
		return CommandResult{}, nil
	default:
		return CommandResult{}, errors.New("unexpected fake command")
	}
}

func fakeAgentID(name string) (AgentID, error) {
	switch filepath.Base(name) {
	case "codex":
		return AgentCodex, nil
	case "claude":
		return AgentClaude, nil
	case "openclaw":
		return AgentOpenClaw, nil
	default:
		return "", errors.New("unknown fake executable")
	}
}

func fakeAction(id AgentID, arguments []string) string {
	if len(arguments) < 2 || arguments[0] != "mcp" {
		return ""
	}
	switch arguments[1] {
	case "get", "show":
		return "get"
	case "add", "set":
		return "add"
	case "remove", "unset":
		return "remove"
	default:
		return ""
	}
}

func TestManagerInstallUpdateAndPurge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config", "rin")
	currentExecutable := filepath.Join(t.TempDir(), "release", executableName("rin"))
	writeTestFile(t, currentExecutable, []byte("rin"), 0o755)
	source := filepath.Join(filepath.Dir(currentExecutable), executableName("rin-mcp"))
	writeTestFile(t, source, []byte("mcp-v1"), 0o755)
	runner := newFakeRunner(AgentCodex, AgentClaude, AgentOpenClaw)
	fixedTime := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	manager, err := New(Options{
		Root:              root,
		CurrentExecutable: currentExecutable,
		Version:           "test-v1",
		Runner:            runner,
		Now:               func() time.Time { return fixedTime },
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := manager.Install(context.Background(), InstallOptions{
		Agents: []AgentID{AgentOpenClaw, AgentCodex, AgentClaude},
		Token:  managerTestToken,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !slices.Equal(report.Agents, supportedAgents) {
		t.Fatalf("agents = %v", report.Agents)
	}
	if !report.Changed {
		t.Fatal("initial install did not report a binary change")
	}
	installed, err := os.ReadFile(report.ServerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != "mcp-v1" {
		t.Fatalf("installed server = %q", installed)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(report.ServerPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("server permissions = %o", info.Mode().Perm())
		}
	}
	assertRegistrationCommands(t, runner.commands, report.ServerPath, report.ConfigPath)
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command.arguments, " "), managerTestToken) {
			t.Fatal("control token leaked into an agent command")
		}
	}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Installed || !status.BinaryPresent || !status.BinaryCurrent ||
		!status.ConfigValid || status.ControlURL != "http://127.0.0.1:7375" {
		t.Fatalf("status = %#v", status)
	}
	for _, agent := range status.Agents {
		if !agent.Available || !agent.Registered || !agent.Managed {
			t.Fatalf("agent status = %#v", agent)
		}
	}
	if _, err := manager.Uninstall(context.Background(), UninstallOptions{
		Agents: []AgentID{AgentCodex},
		Purge:  true,
	}); err == nil || !strings.Contains(err.Error(), "every managed Agent") {
		t.Fatalf("partial purge error = %v", err)
	}
	for _, id := range supportedAgents {
		if !runner.registered[id] {
			t.Fatalf("partial purge removed %s", id)
		}
	}

	commandCount := len(runner.commands)
	updateSource := filepath.Join(t.TempDir(), executableName("rin-mcp"))
	writeTestFile(t, updateSource, []byte("mcp-v2"), 0o755)
	updated, err := manager.Update(context.Background(), UpdateOptions{
		ServerSource: updateSource,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Hash == report.Hash {
		t.Fatal("binary hash did not change")
	}
	if !updated.Changed {
		t.Fatal("binary update did not report a change")
	}
	if len(runner.commands) != commandCount {
		t.Fatal("binary update rewrote agent registrations")
	}
	unchanged, err := manager.Update(context.Background(), UpdateOptions{
		ServerSource: updateSource,
	})
	if err != nil {
		t.Fatalf("unchanged Update: %v", err)
	}
	if unchanged.Changed {
		t.Fatal("same binary was replaced again")
	}

	if _, err := manager.Uninstall(context.Background(), UninstallOptions{
		Purge: true,
	}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !containsFakeCommand(
		runner.commands,
		"claude mcp remove --scope user rin",
	) {
		t.Fatal("Claude user-scoped registration was not removed explicitly")
	}
	for _, path := range []string{
		manager.paths.Config,
		manager.paths.Manifest,
		manager.paths.Server,
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed path still exists: %s", path)
		}
	}
}

func TestManagerAddsAgentWithoutOriginalDistribution(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rin")
	source := filepath.Join(t.TempDir(), executableName("rin-mcp"))
	writeTestFile(t, source, []byte("mcp"), 0o755)
	runner := newFakeRunner(AgentCodex, AgentClaude)
	manager, err := New(Options{
		Root:              root,
		CurrentExecutable: filepath.Join(t.TempDir(), executableName("rin")),
		Runner:            runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), InstallOptions{
		Agents:       []AgentID{AgentCodex},
		Token:        managerTestToken,
		ServerSource: source,
	}); err != nil {
		t.Fatalf("initial Install: %v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Install(context.Background(), InstallOptions{
		Agents: []AgentID{AgentClaude},
	})
	if err != nil {
		t.Fatalf("Install with managed binary: %v", err)
	}
	if report.Changed {
		t.Fatal("managed binary was unexpectedly replaced")
	}
	if !slices.Equal(report.Agents, []AgentID{AgentCodex, AgentClaude}) {
		t.Fatalf("managed agents = %v", report.Agents)
	}
}

func containsFakeCommand(commands []fakeCommand, expected string) bool {
	for _, command := range commands {
		actual := filepath.Base(command.name) + " " + strings.Join(command.arguments, " ")
		if actual == expected {
			return true
		}
	}
	return false
}

func TestManagerProtectsUnmanagedRegistration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rin")
	source := filepath.Join(t.TempDir(), executableName("rin-mcp"))
	writeTestFile(t, source, []byte("mcp"), 0o755)
	runner := newFakeRunner(AgentCodex)
	runner.registered[AgentCodex] = true
	manager, err := New(Options{
		Root:              root,
		CurrentExecutable: filepath.Join(t.TempDir(), executableName("rin")),
		Runner:            runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Install(context.Background(), InstallOptions{
		Agents:       []AgentID{AgentCodex},
		Token:        managerTestToken,
		ServerSource: source,
	})
	if err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("unmanaged registration error = %v", err)
	}
	if _, err := os.Stat(manager.paths.Manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed preflight modified the installation")
	}
	_, err = manager.Install(context.Background(), InstallOptions{
		Agents:       []AgentID{AgentCodex},
		Token:        managerTestToken,
		ServerSource: source,
		Force:        true,
	})
	if err != nil {
		t.Fatalf("forced Install: %v", err)
	}
	if !runner.registered[AgentCodex] {
		t.Fatal("forced install did not restore registration")
	}
}

func TestManagerRejectsAgentInspectionFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rin")
	runner := newFakeRunner(AgentCodex)
	runner.failAction = "get"
	runner.failAgent = AgentCodex
	manager, err := New(Options{
		Root:              root,
		CurrentExecutable: filepath.Join(t.TempDir(), executableName("rin")),
		Runner:            runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Install(context.Background(), InstallOptions{
		Agents: []AgentID{AgentCodex},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated failure") {
		t.Fatalf("inspection error = %v", err)
	}
	if _, err := os.Stat(manager.paths.Manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection failure modified the installation")
	}
}

func TestManagerRejectsConcurrentWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rin")
	manager, err := New(Options{
		Root:              root,
		CurrentExecutable: filepath.Join(t.TempDir(), executableName("rin")),
		Runner:            newFakeRunner(AgentCodex),
	})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireInstallLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseInstallLock(lock)
	_, err = manager.Install(context.Background(), InstallOptions{
		Agents: []AgentID{AgentCodex},
	})
	if !errors.Is(err, ErrInstallerLocked) {
		t.Fatalf("concurrent writer error = %v", err)
	}
}

func TestManagerKeepsPartialInstallOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rin")
	source := filepath.Join(t.TempDir(), executableName("rin-mcp"))
	writeTestFile(t, source, []byte("mcp"), 0o755)
	runner := newFakeRunner(AgentCodex, AgentClaude)
	runner.failAction = "add"
	runner.failAgent = AgentClaude
	manager, err := New(Options{
		Root:              root,
		CurrentExecutable: filepath.Join(t.TempDir(), executableName("rin")),
		Runner:            runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Install(context.Background(), InstallOptions{
		Agents:       []AgentID{AgentCodex, AgentClaude},
		Token:        managerTestToken,
		ServerSource: source,
	})
	if err == nil || !strings.Contains(err.Error(), "simulated failure") {
		t.Fatalf("partial install error = %v", err)
	}
	current, exists, err := manager.loadManifest()
	if err != nil || !exists {
		t.Fatalf("loadManifest = %#v, %v, %v", current, exists, err)
	}
	if !slices.Equal(current.Agents, []AgentID{AgentCodex}) {
		t.Fatalf("owned agents = %v", current.Agents)
	}
}

func assertRegistrationCommands(
	t *testing.T,
	commands []fakeCommand,
	serverPath string,
	configPath string,
) {
	t.Helper()
	openClawDefinition, err := json.Marshal(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{
		Command: serverPath,
		Args:    []string{"--config", configPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := make([]string, 0, len(commands))
	for _, command := range commands {
		joined = append(joined, filepath.Base(command.name)+" "+strings.Join(command.arguments, " "))
	}
	expected := []string{
		"codex mcp add rin -- " + serverPath + " --config " + configPath,
		"claude mcp add --transport stdio --scope user rin -- " + serverPath + " --config " + configPath,
		"openclaw mcp set rin " + string(openClawDefinition),
	}
	for _, value := range expected {
		if !slices.Contains(joined, value) {
			t.Fatalf("missing command %q in %v", value, joined)
		}
	}
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

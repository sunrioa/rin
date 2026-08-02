package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sunrioa/rin/internal/mcpinstall"
)

func runMCP(
	ctx context.Context,
	arguments []string,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
) error {
	if len(arguments) == 0 {
		return writeMCPHelp(output)
	}
	manager, err := mcpinstall.New(mcpinstall.Options{Version: version})
	if err != nil {
		return err
	}
	switch arguments[0] {
	case "install", "configure":
		return runMCPInstall(
			ctx,
			manager,
			arguments[1:],
			input,
			output,
			errorOutput,
		)
	case "status":
		return runMCPStatus(ctx, manager, arguments[1:], output, errorOutput)
	case "update":
		return runMCPUpdate(ctx, manager, arguments[1:], output, errorOutput)
	case "uninstall":
		return runMCPUninstall(ctx, manager, arguments[1:], output, errorOutput)
	case "help", "-h", "--help":
		return writeMCPHelp(output)
	default:
		return fmt.Errorf("unsupported mcp command %q", arguments[0])
	}
}

func runMCPInstall(
	ctx context.Context,
	manager *mcpinstall.Manager,
	arguments []string,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("rin mcp install", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	agentList := flags.String(
		"agents",
		"",
		"comma-separated agents: codex, claude, openclaw",
	)
	allDetected := flags.Bool("yes", false, "install for all detected agents")
	serverSource := flags.String("server", "", "source rin-mcp executable")
	controlURL := flags.String(
		"control-url",
		os.Getenv("RIN_CONTROL_URL"),
		"loopback rin-control base URL",
	)
	force := flags.Bool("force", false, "replace an unmanaged rin registration")
	repair := flags.Bool("repair", false, "rewrite managed agent registrations")
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), `Usage:
  rin mcp install [options]

RIN_CONTROL_TOKEN supplies the local daemon token without exposing it in
shell history. Without -agents, an interactive agent selector is shown.

Options:
`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *allDetected && *agentList != "" {
		return errors.New("-yes and -agents cannot be used together")
	}
	var agents []mcpinstall.AgentID
	var err error
	switch {
	case *agentList != "":
		agents, err = parseAgentList(*agentList)
	case *allDetected:
		agents = availableAgents(manager.Detect(ctx))
	default:
		agents, err = promptForAgents(input, output, manager.Detect(ctx))
	}
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		return errors.New("no supported MCP agent was detected or selected")
	}
	report, err := manager.Install(ctx, mcpinstall.InstallOptions{
		Agents:       agents,
		ControlURL:   *controlURL,
		Token:        os.Getenv("RIN_CONTROL_TOKEN"),
		ServerSource: *serverSource,
		Force:        *force,
		Repair:       *repair,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(
		output,
		"Rin MCP installed for %s.\nServer: %s\nConfig: %s\n",
		formatAgents(report.Agents),
		report.ServerPath,
		report.ConfigPath,
	)
	fmt.Fprintln(output, "Restart or reload the selected Agent clients before use.")
	return nil
}

func runMCPStatus(
	ctx context.Context,
	manager *mcpinstall.Manager,
	arguments []string,
	output io.Writer,
	errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("rin mcp status", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	status, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Installed: %t\n", status.Installed)
	if status.InstallerVersion != "" {
		fmt.Fprintf(output, "Installer version: %s\n", status.InstallerVersion)
	}
	if !status.UpdatedAt.IsZero() {
		fmt.Fprintf(output, "Updated: %s\n", status.UpdatedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(output, "Server: %s\n", status.ServerPath)
	fmt.Fprintf(
		output,
		"Binary: present=%t verified=%t\n",
		status.BinaryPresent,
		status.BinaryCurrent,
	)
	fmt.Fprintf(output, "Config: %s (valid=%t)\n", status.ConfigPath, status.ConfigValid)
	if status.ControlURL != "" {
		fmt.Fprintf(output, "Control URL: %s\n", status.ControlURL)
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "AGENT\tCOMMAND\tREGISTERED\tMANAGED\tDETAIL")
	for _, agent := range status.Agents {
		detail := agent.Error
		if detail == "" {
			detail = "ok"
		}
		fmt.Fprintf(
			writer,
			"%s\t%t\t%t\t%t\t%s\n",
			agent.Name,
			agent.Available,
			agent.Registered,
			agent.Managed,
			detail,
		)
	}
	return writer.Flush()
}

func runMCPUpdate(
	ctx context.Context,
	manager *mcpinstall.Manager,
	arguments []string,
	output io.Writer,
	errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("rin mcp update", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	serverSource := flags.String("server", "", "source rin-mcp executable")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	report, err := manager.Update(ctx, mcpinstall.UpdateOptions{
		ServerSource: *serverSource,
	})
	if err != nil {
		return err
	}
	if report.Changed {
		fmt.Fprintf(output, "Rin MCP updated: %s\n", report.ServerPath)
	} else {
		fmt.Fprintf(output, "Rin MCP is already current: %s\n", report.ServerPath)
	}
	fmt.Fprintln(output, "Agent registrations and the private connection config were preserved.")
	return nil
}

func runMCPUninstall(
	ctx context.Context,
	manager *mcpinstall.Manager,
	arguments []string,
	output io.Writer,
	errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("rin mcp uninstall", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	agentList := flags.String(
		"agents",
		"",
		"comma-separated managed agents; default removes all",
	)
	purge := flags.Bool("purge", false, "also remove managed binary and private config")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	var agents []mcpinstall.AgentID
	var err error
	if *agentList != "" {
		agents, err = parseAgentList(*agentList)
		if err != nil {
			return err
		}
	}
	report, err := manager.Uninstall(ctx, mcpinstall.UninstallOptions{
		Agents: agents,
		Purge:  *purge,
	})
	if err != nil {
		return err
	}
	if *purge {
		fmt.Fprintln(output, "Rin MCP registrations, managed binary, and private config removed.")
		return nil
	}
	fmt.Fprintf(output, "Rin MCP registrations removed. Remaining: %s\n", formatAgents(report.Agents))
	return nil
}

func parseAgentList(value string) ([]mcpinstall.AgentID, error) {
	parts := strings.Split(value, ",")
	result := make([]mcpinstall.AgentID, 0, len(parts))
	seen := make(map[mcpinstall.AgentID]bool, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		id, err := mcpinstall.ParseAgentID(part)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result, nil
}

func promptForAgents(
	input io.Reader,
	output io.Writer,
	statuses []mcpinstall.AgentStatus,
) ([]mcpinstall.AgentID, error) {
	available := availableAgents(statuses)
	if len(available) == 0 {
		return nil, errors.New(
			"no supported Agent CLI was detected; install one or use -agents after adding it to PATH",
		)
	}
	fmt.Fprintln(output, "Select the Agent clients that should use Rin MCP:")
	for index, id := range available {
		fmt.Fprintf(output, "  %d. %s\n", index+1, mcpinstall.AgentDisplayName(id))
	}
	fmt.Fprint(output, "Selection (comma-separated numbers or names; Enter selects all): ")
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read agent selection: %w", err)
	}
	line = strings.TrimSpace(line)
	if errors.Is(err, io.EOF) && line == "" {
		return nil, errors.New(
			"noninteractive install requires -agents or -yes",
		)
	}
	if line == "" {
		return available, nil
	}
	result := make([]mcpinstall.AgentID, 0, len(available))
	seen := make(map[mcpinstall.AgentID]bool, len(available))
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		position, parseErr := strconv.Atoi(part)
		var id mcpinstall.AgentID
		if parseErr == nil {
			if position < 1 || position > len(available) {
				return nil, fmt.Errorf("agent selection %d is out of range", position)
			}
			id = available[position-1]
		} else {
			id, err = mcpinstall.ParseAgentID(part)
			if err != nil {
				return nil, err
			}
			if !containsSelectedAgent(available, id) {
				return nil, fmt.Errorf("%s CLI was not detected", mcpinstall.AgentDisplayName(id))
			}
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result, nil
}

func availableAgents(statuses []mcpinstall.AgentStatus) []mcpinstall.AgentID {
	result := make([]mcpinstall.AgentID, 0, len(statuses))
	for _, status := range statuses {
		if status.Available {
			result = append(result, status.ID)
		}
	}
	return result
}

func containsSelectedAgent(values []mcpinstall.AgentID, target mcpinstall.AgentID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func formatAgents(agents []mcpinstall.AgentID) string {
	if len(agents) == 0 {
		return "none"
	}
	names := make([]string, 0, len(agents))
	for _, id := range agents {
		names = append(names, mcpinstall.AgentDisplayName(id))
	}
	return strings.Join(names, ", ")
}

func writeMCPHelp(output io.Writer) error {
	_, err := io.WriteString(output, `Usage:
  rin mcp install [options]
  rin mcp status
  rin mcp update [-server PATH]
  rin mcp uninstall [-agents LIST] [-purge]

Supported Agent clients: Codex, Claude Code, and OpenClaw.
Run "rin mcp install --help" for installer options.
`)
	return err
}

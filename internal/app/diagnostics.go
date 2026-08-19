package app

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/internal/mcpinstall"
	"github.com/sunrioa/rin/managementapi"
	"github.com/sunrioa/rin/policy"
)

type diagnosticsDependencies struct {
	Control       *controlplane.Service
	Policy        *policy.Engine
	Config        configuration
	AgentConfig   agentdaemon.Config
	InternalAgent *agentdaemon.Daemon
	MCP           *mcpinstall.Manager
	MCPError      error
}

func newDiagnosticsProvider(dependencies diagnosticsDependencies) managementapi.DiagnosticsProvider {
	return func(ctx context.Context) (managementapi.DiagnosticsSnapshot, error) {
		return collectDiagnostics(ctx, dependencies), nil
	}
}

func collectDiagnostics(ctx context.Context, dependencies diagnosticsDependencies) managementapi.DiagnosticsSnapshot {
	now := time.Now()
	result := managementapi.DiagnosticsSnapshot{
		CheckedAt:   now.UnixMilli(),
		Connections: []managementapi.ConnectionDiagnostic{},
		Model:       modelMetadata(dependencies.Config, dependencies.AgentConfig),
		Memory:      memoryMetadata(dependencies.Config, dependencies.AgentConfig),
		Policy:      policyMetadata(dependencies.Policy),
		Permissions: managementapi.PermissionMetadata{
			PrincipalID:   dependencies.Config.principal.ID,
			ControlScopes: cloneStrings(dependencies.Config.principal.GrantedScopes),
			ConsoleScopes: cloneStrings(managementPrincipal(dependencies.Config.principal).GrantedScopes),
		},
		InternalAgent: agentMetadata(dependencies.Config, dependencies.InternalAgent),
		MCP:           mcpMetadata(ctx, dependencies.MCP, dependencies.MCPError),
	}
	sort.Strings(result.Permissions.ControlScopes)
	sort.Strings(result.Permissions.ConsoleScopes)
	result.Connections = append(result.Connections,
		internalAgentConnection(result.InternalAgent),
		mcpConnection(result.MCP),
	)

	if dependencies.Control == nil {
		result.Connections = append(result.Connections, managementapi.ConnectionDiagnostic{
			ID: "control-plane", Kind: "control", Status: managementapi.DiagnosticError,
			Detail: "Control Plane 未配置",
		})
		return result
	}
	started := time.Now()
	worlds, err := dependencies.Control.ListWorlds(managementPrincipal(dependencies.Config.principal))
	latency := time.Since(started).Milliseconds()
	if err != nil {
		result.Connections = append(result.Connections, managementapi.ConnectionDiagnostic{
			ID: "control-plane", Kind: "control", Status: managementapi.DiagnosticError,
			Detail:   "读取 Control Plane 状态失败: " + err.Error(),
			Endpoint: controlEndpoint(dependencies.Config.address), LatencyMillis: latency,
		})
		return result
	}
	result.Connections = append(result.Connections, managementapi.ConnectionDiagnostic{
		ID: "control-plane", Kind: "control", Status: managementapi.DiagnosticOK,
		Detail: "Control Plane 可响应", Endpoint: controlEndpoint(dependencies.Config.address),
		LatencyMillis: latency, Worlds: uint32(len(worlds)),
	})

	hosts := make(map[string]*managementapi.ConnectionDiagnostic)
	for _, world := range worlds {
		entry := hosts[world.HostID]
		if entry == nil {
			entry = &managementapi.ConnectionDiagnostic{
				ID: "host:" + world.HostID, Kind: "game-host", Status: managementapi.DiagnosticOffline,
				Detail: "没有在线世界", Endpoint: world.HostID,
			}
			hosts[world.HostID] = entry
		}
		entry.Worlds++
		if world.Online {
			entry.Status = managementapi.DiagnosticOK
			entry.Detail = "至少一个世界在线"
		}
		actors, actorErr := dependencies.Control.ListActors(
			managementPrincipal(dependencies.Config.principal), world.HostID, world.WorldID,
		)
		if actorErr == nil {
			entry.Actors += uint32(len(actors))
		}
	}
	hostIDs := make([]string, 0, len(hosts))
	for id := range hosts {
		hostIDs = append(hostIDs, id)
	}
	sort.Strings(hostIDs)
	for _, id := range hostIDs {
		result.Connections = append(result.Connections, *hosts[id])
	}
	return result
}

func modelMetadata(config configuration, agentConfig agentdaemon.Config) managementapi.ModelConfigMetadata {
	if config.agentConfig == "" {
		return managementapi.ModelConfigMetadata{}
	}
	model := agentConfig.Model
	return managementapi.ModelConfigMetadata{
		Enabled:              true,
		Provider:             model.Provider,
		Endpoint:             redactEndpoint(model.BaseURL),
		Model:                model.Model,
		ResponseFormat:       model.ResponseFormat,
		ThinkingMode:         model.ThinkingMode,
		Authentication:       model.Authentication,
		CredentialConfigured: config.agentAPIKey != "",
	}
}

func memoryMetadata(config configuration, agentConfig agentdaemon.Config) managementapi.MemoryConfigMetadata {
	embedding := agentConfig.Memory.SemanticEmbedding
	return managementapi.MemoryConfigMetadata{
		Backend:                      "sqlite",
		SemanticEmbeddingEnabled:     embedding.Enabled,
		SemanticProvider:             embedding.Provider,
		SemanticEndpoint:             redactEndpoint(embedding.BaseURL),
		SemanticModel:                embedding.Model,
		SemanticCredentialConfigured: config.agentEmbeddingAPIKey != "",
	}
}

func policyMetadata(engine *policy.Engine) managementapi.PolicyConfigMetadata {
	if engine == nil {
		return managementapi.PolicyConfigMetadata{}
	}
	config := engine.Config()
	return managementapi.PolicyConfigMetadata{
		Revision:           config.Revision,
		Profile:            string(config.Profile),
		RuleCount:          uint32(len(config.Rules)),
		BudgetCount:        uint32(len(config.Budgets)),
		KnownEffectKinds:   cloneStrings(config.KnownEffectKinds),
		KnownScopes:        cloneStrings(config.KnownScopes),
		ConfirmationScopes: cloneStrings(config.ConfirmationScopes),
	}
}

func agentMetadata(config configuration, agent *agentdaemon.Daemon) managementapi.AgentConfigMetadata {
	if config.agentConfig == "" || agent == nil {
		return managementapi.AgentConfigMetadata{Status: managementapi.DiagnosticDisabled}
	}
	return managementapi.AgentConfigMetadata{
		Enabled:                       true,
		Status:                        managementapi.DiagnosticOK,
		ModelCredentialConfigured:     config.agentAPIKey != "",
		EmbeddingCredentialConfigured: config.agentEmbeddingAPIKey != "",
	}
}

func internalAgentConnection(metadata managementapi.AgentConfigMetadata) managementapi.ConnectionDiagnostic {
	detail := "内部 Agent 未启用"
	if metadata.Enabled {
		detail = "内部 Agent Runtime 正常"
	}
	return managementapi.ConnectionDiagnostic{
		ID: "internal-agent", Kind: "agent", Status: metadata.Status, Detail: detail,
	}
}

func mcpMetadata(ctx context.Context, manager *mcpinstall.Manager, managerErr error) managementapi.MCPConfigMetadata {
	result := managementapi.MCPConfigMetadata{Commands: []managementapi.MCPCommand{
		{ID: "install", Label: "安装或修复 MCP", Command: "rin mcp install"},
		{ID: "update", Label: "更新 MCP", Command: "rin mcp update"},
		{ID: "status", Label: "检查 MCP 状态", Command: "rin mcp status"},
		{ID: "doctor", Label: "诊断本机环境", Command: "rin doctor"},
	}}
	if managerErr != nil {
		result.Error = managerErr.Error()
		return result
	}
	if manager == nil {
		result.Error = "MCP 安装器未配置"
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := manager.Status(probeCtx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Installed = status.Installed
	result.BinaryPresent = status.BinaryPresent
	result.BinaryCurrent = status.BinaryCurrent
	result.ConfigValid = status.ConfigValid
	result.ControlURL = redactEndpoint(status.ControlURL)
	result.Agents = make([]managementapi.MCPAgentMetadata, 0, len(status.Agents))
	for _, agent := range status.Agents {
		result.Agents = append(result.Agents, managementapi.MCPAgentMetadata{
			ID: string(agent.ID), Name: agent.Name, Available: agent.Available,
			Registered: agent.Registered, Managed: agent.Managed, Error: agent.Error,
		})
	}
	return result
}

func mcpConnection(metadata managementapi.MCPConfigMetadata) managementapi.ConnectionDiagnostic {
	status := managementapi.DiagnosticWarning
	detail := "MCP 尚未安装"
	if metadata.Error != "" {
		status = managementapi.DiagnosticError
		detail = metadata.Error
	} else if metadata.Installed && metadata.ConfigValid && metadata.BinaryCurrent {
		status = managementapi.DiagnosticOK
		detail = "MCP 已安装且配置有效"
	} else if metadata.Installed {
		detail = "MCP 已安装，但需要检查配置或版本"
	}
	return managementapi.ConnectionDiagnostic{
		ID: "mcp", Kind: "mcp", Status: status, Detail: detail,
		Endpoint: metadata.ControlURL,
	}
}

func controlEndpoint(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	return "http://" + address
}

func redactEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

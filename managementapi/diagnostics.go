package managementapi

import (
	"context"
	"errors"
)

var ErrDiagnosticsUnavailable = errors.New("runtime diagnostics are not enabled")

const (
	DiagnosticOK       = "ok"
	DiagnosticWarning  = "warning"
	DiagnosticOffline  = "offline"
	DiagnosticDisabled = "disabled"
	DiagnosticError    = "error"
)

// ConnectionDiagnostic is a short, human-readable projection of one local
// connection. It intentionally contains no credentials or private payloads.
type ConnectionDiagnostic struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	LatencyMillis int64  `json:"latency_millis,omitempty"`
	Worlds        uint32 `json:"worlds,omitempty"`
	Actors        uint32 `json:"actors,omitempty"`
}

type ModelConfigMetadata struct {
	Enabled              bool   `json:"enabled"`
	Provider             string `json:"provider,omitempty"`
	Endpoint             string `json:"endpoint,omitempty"`
	Model                string `json:"model,omitempty"`
	ResponseFormat       string `json:"response_format,omitempty"`
	ThinkingMode         string `json:"thinking_mode,omitempty"`
	Authentication       string `json:"authentication,omitempty"`
	CredentialConfigured bool   `json:"credential_configured"`
}

type MemoryConfigMetadata struct {
	Backend                      string `json:"backend"`
	SemanticEmbeddingEnabled     bool   `json:"semantic_embedding_enabled"`
	SemanticProvider             string `json:"semantic_provider,omitempty"`
	SemanticEndpoint             string `json:"semantic_endpoint,omitempty"`
	SemanticModel                string `json:"semantic_model,omitempty"`
	SemanticCredentialConfigured bool   `json:"semantic_credential_configured"`
}

type PolicyConfigMetadata struct {
	Revision           uint64   `json:"revision"`
	Profile            string   `json:"profile"`
	RuleCount          uint32   `json:"rule_count"`
	BudgetCount        uint32   `json:"budget_count"`
	KnownEffectKinds   []string `json:"known_effect_kinds,omitempty"`
	KnownScopes        []string `json:"known_scopes,omitempty"`
	ConfirmationScopes []string `json:"confirmation_scopes,omitempty"`
}

type PermissionMetadata struct {
	PrincipalID   string   `json:"principal_id"`
	ControlScopes []string `json:"control_scopes,omitempty"`
	ConsoleScopes []string `json:"console_scopes,omitempty"`
}

type AgentConfigMetadata struct {
	Enabled                       bool   `json:"enabled"`
	Status                        string `json:"status"`
	ModelCredentialConfigured     bool   `json:"model_credential_configured"`
	EmbeddingCredentialConfigured bool   `json:"embedding_credential_configured"`
}

type MCPAgentMetadata struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Available  bool   `json:"available"`
	Registered bool   `json:"registered"`
	Managed    bool   `json:"managed"`
	Error      string `json:"error,omitempty"`
}

type MCPCommand struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`
}

type MCPConfigMetadata struct {
	Installed     bool               `json:"installed"`
	BinaryPresent bool               `json:"binary_present"`
	BinaryCurrent bool               `json:"binary_current"`
	ConfigValid   bool               `json:"config_valid"`
	ControlURL    string             `json:"control_url,omitempty"`
	Agents        []MCPAgentMetadata `json:"agents,omitempty"`
	Commands      []MCPCommand       `json:"commands"`
	Error         string             `json:"error,omitempty"`
}

// DiagnosticsSnapshot is the Console-safe runtime and configuration view.
// It is deliberately metadata-only: secrets, prompts and private memory are
// never part of this contract.
type DiagnosticsSnapshot struct {
	CheckedAt     int64                  `json:"checked_at_unix_millis"`
	Connections   []ConnectionDiagnostic `json:"connections"`
	Model         ModelConfigMetadata    `json:"model"`
	Memory        MemoryConfigMetadata   `json:"memory"`
	Policy        PolicyConfigMetadata   `json:"policy"`
	Permissions   PermissionMetadata     `json:"permissions"`
	InternalAgent AgentConfigMetadata    `json:"internal_agent"`
	MCP           MCPConfigMetadata      `json:"mcp"`
}

type DiagnosticsProvider func(context.Context) (DiagnosticsSnapshot, error)

func (service *Service) ConfigureDiagnostics(provider DiagnosticsProvider) error {
	if provider == nil {
		return errors.New("diagnostics provider is required")
	}
	service.diagnostics = provider
	return nil
}

func (service *Service) Diagnostics(ctx context.Context) (DiagnosticsSnapshot, error) {
	if service.diagnostics == nil {
		return DiagnosticsSnapshot{}, ErrDiagnosticsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return DiagnosticsSnapshot{}, err
	}
	return service.diagnostics(ctx)
}

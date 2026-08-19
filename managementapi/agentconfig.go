package managementapi

import (
	"context"
	"errors"

	"github.com/sunrioa/rin/agentdaemon"
)

var (
	ErrAgentConfigUnavailable = errors.New("Agent configuration management is not enabled")
	ErrInvalidAgentConfig     = errors.New("invalid Agent configuration")
)

// AgentConfigSnapshot is the Console-safe projection of the internal Agent
// configuration. Model settings are editable; credentials are represented by
// presence only and never cross the response boundary.
type AgentConfigSnapshot struct {
	Configured           bool                    `json:"configured"`
	Model                agentdaemon.ModelConfig `json:"model"`
	CredentialConfigured bool                    `json:"credential_configured"`
}

// AgentConfigSaveRequest uses a tri-state credential update:
// nil APIKey keeps the current secret, a non-nil APIKey sets it, and
// ClearAPIKey removes it. The secret is request-only and is never serialized
// into AgentConfigSnapshot.
type AgentConfigSaveRequest struct {
	Model       agentdaemon.ModelConfig `json:"model"`
	APIKey      *string                 `json:"api_key,omitempty"`
	ClearAPIKey bool                    `json:"clear_api_key,omitempty"`
}

type AgentConfigSaveResponse struct {
	AgentConfigSnapshot
	RequiresRestart bool `json:"requires_restart"`
}

// AgentConfigEditor is implemented by the application-owned private file
// store. Management API remains unaware of paths, secrets, and process
// restart mechanics.
type AgentConfigEditor interface {
	AgentConfig(context.Context) (AgentConfigSnapshot, error)
	SaveAgentConfig(context.Context, AgentConfigSaveRequest) (AgentConfigSaveResponse, error)
}

func (service *Service) ConfigureAgentConfig(editor AgentConfigEditor) error {
	if editor == nil {
		return errors.New("Agent configuration editor is required")
	}
	service.agentConfig = editor
	return nil
}

func (service *Service) AgentConfig(ctx context.Context) (AgentConfigSnapshot, error) {
	if service.agentConfig == nil {
		return AgentConfigSnapshot{}, ErrAgentConfigUnavailable
	}
	return service.agentConfig.AgentConfig(ctx)
}

func (service *Service) SaveAgentConfig(
	ctx context.Context,
	request AgentConfigSaveRequest,
) (AgentConfigSaveResponse, error) {
	if service.agentConfig == nil {
		return AgentConfigSaveResponse{}, ErrAgentConfigUnavailable
	}
	return service.agentConfig.SaveAgentConfig(ctx, request)
}

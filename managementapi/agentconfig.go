package managementapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/cognition"
)

var (
	ErrAgentConfigUnavailable = errors.New("Agent configuration management is not enabled")
	ErrInvalidAgentConfig     = errors.New("invalid Agent configuration")
)

// AgentConfigSnapshot is the Console-safe projection of the internal Agent
// configuration. Model settings are editable; credentials are represented by
// presence only and never cross the response boundary.
type AgentConfigSnapshot struct {
	Configured                    bool                       `json:"configured"`
	Model                         agentdaemon.ModelConfig    `json:"model"`
	Memory                        agentdaemon.MemoryConfig   `json:"memory"`
	Lookahead                     cognition.LookaheadOptions `json:"lookahead"`
	CredentialConfigured          bool                       `json:"credential_configured"`
	EmbeddingCredentialConfigured bool                       `json:"embedding_credential_configured"`
}

// AgentConfigSaveRequest uses tri-state credential updates: nil preserves a
// secret, a non-nil value sets it, and the matching clear flag removes it.
// Secrets are request-only and are never serialized into AgentConfigSnapshot.
type AgentConfigSaveRequest struct {
	Model                agentdaemon.ModelConfig     `json:"model"`
	Memory               *agentdaemon.MemoryConfig   `json:"memory"`
	Lookahead            *cognition.LookaheadOptions `json:"lookahead,omitempty"`
	APIKey               *string                     `json:"api_key,omitempty"`
	ClearAPIKey          bool                        `json:"clear_api_key,omitempty"`
	EmbeddingAPIKey      *string                     `json:"embedding_api_key,omitempty"`
	ClearEmbeddingAPIKey bool                        `json:"clear_embedding_api_key,omitempty"`
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
	if request.Memory == nil {
		return AgentConfigSaveResponse{}, fmt.Errorf(
			"%w: memory configuration is required", ErrInvalidAgentConfig,
		)
	}
	return service.agentConfig.SaveAgentConfig(ctx, request)
}

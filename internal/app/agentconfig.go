package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/internal/privatefile"
	"github.com/sunrioa/rin/managementapi"
)

const maxAgentSecretBytes int64 = 64 << 10

type agentSecrets struct {
	AgentToken      string `json:"agent_token,omitempty"`
	APIKey          string `json:"api_key,omitempty"`
	EmbeddingAPIKey string `json:"embedding_api_key,omitempty"`
}

type agentConfigStore struct {
	mu                               sync.Mutex
	configPath                       string
	secretsPath                      string
	config                           agentdaemon.Config
	configured                       bool
	secrets                          agentSecrets
	credentialConfigured             bool
	embeddingCredentialConfigured    bool
	credentialOverrideByEnv          bool
	embeddingCredentialOverrideByEnv bool
}

func managedAgentConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "agent", "agent-config.json")
}

func managedAgentSecretsPath(dataDir string) string {
	return filepath.Join(dataDir, "agent", "agent-secrets.json")
}

func openAgentConfigStore(dataDir, runtimePath string) (*agentConfigStore, error) {
	configPath := managedAgentConfigPath(dataDir)
	if strings.TrimSpace(runtimePath) != "" {
		configPath = runtimePath
	}
	store := &agentConfigStore{
		configPath:  configPath,
		secretsPath: managedAgentSecretsPath(dataDir),
		config:      defaultAgentConfig(),
	}
	if _, err := os.Stat(configPath); err == nil {
		config, loadErr := agentdaemon.LoadConfig(configPath)
		if loadErr != nil {
			return nil, loadErr
		}
		store.config = config
		store.configured = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect Agent configuration: %w", err)
	}
	if err := privatefile.ReadJSON(store.secretsPath, maxAgentSecretBytes, &store.secrets); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load Agent secrets: %w", err)
	}
	store.credentialConfigured = store.secrets.APIKey != ""
	store.embeddingCredentialConfigured = store.secrets.EmbeddingAPIKey != ""
	return store, nil
}

func defaultAgentConfig() agentdaemon.Config {
	persona := cognition.DefaultPersonaSnapshot()
	return agentdaemon.Config{
		ContractVersion: agentdaemon.ConfigVersion,
		Model: agentdaemon.ModelConfig{
			Provider:       agentdaemon.ProviderOpenAICompatible,
			ResponseFormat: "json_schema",
			Authentication: agentdaemon.AuthenticationBearerEnv,
		},
		Personas:        persona.Profiles,
		PersonaBindings: persona.Bindings,
	}
}

func (store *agentConfigStore) AgentConfig(ctx context.Context) (managementapi.AgentConfigSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return managementapi.AgentConfigSnapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshotLocked(), nil
}

func (store *agentConfigStore) snapshotLocked() managementapi.AgentConfigSnapshot {
	lookahead, _ := cognition.NormalizeLookaheadOptions(store.config.Runtime.Lookahead)
	return managementapi.AgentConfigSnapshot{
		Configured:                    store.configured,
		Model:                         store.config.Model,
		Memory:                        store.config.Memory,
		Lookahead:                     lookahead,
		CredentialConfigured:          store.credentialConfigured,
		EmbeddingCredentialConfigured: store.embeddingCredentialConfigured,
	}
}

func (store *agentConfigStore) SaveAgentConfig(
	ctx context.Context,
	request managementapi.AgentConfigSaveRequest,
) (managementapi.AgentConfigSaveResponse, error) {
	if err := ctx.Err(); err != nil {
		return managementapi.AgentConfigSaveResponse{}, err
	}
	if request.Memory == nil {
		return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
			"%w: memory configuration is required",
			managementapi.ErrInvalidAgentConfig,
		)
	}
	if request.APIKey != nil && request.ClearAPIKey {
		return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
			"%w: api_key and clear_api_key cannot be used together",
			managementapi.ErrInvalidAgentConfig,
		)
	}
	if request.EmbeddingAPIKey != nil && request.ClearEmbeddingAPIKey {
		return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
			"%w: embedding_api_key and clear_embedding_api_key cannot be used together",
			managementapi.ErrInvalidAgentConfig,
		)
	}
	if request.APIKey != nil {
		if strings.TrimSpace(*request.APIKey) == "" {
			return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
				"%w: api_key must not be empty; use clear_api_key to remove it",
				managementapi.ErrInvalidAgentConfig,
			)
		}
		if len(*request.APIKey) > 16<<10 {
			return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
				"%w: api_key exceeds 16384 bytes",
				managementapi.ErrInvalidAgentConfig,
			)
		}
	}
	if request.EmbeddingAPIKey != nil {
		if strings.TrimSpace(*request.EmbeddingAPIKey) == "" {
			return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
				"%w: embedding_api_key must not be empty; use clear_embedding_api_key to remove it",
				managementapi.ErrInvalidAgentConfig,
			)
		}
		if len(*request.EmbeddingAPIKey) > 16<<10 {
			return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
				"%w: embedding_api_key exceeds 16384 bytes",
				managementapi.ErrInvalidAgentConfig,
			)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	config := store.config
	config.Model = request.Model
	config.Memory = *request.Memory
	if request.Lookahead != nil {
		lookahead := *request.Lookahead
		config.Runtime.Lookahead = &lookahead
	}
	validated, err := agentdaemon.ValidateConfig(config)
	if err != nil {
		return managementapi.AgentConfigSaveResponse{}, fmt.Errorf(
			"%w: %v", managementapi.ErrInvalidAgentConfig, err,
		)
	}
	secrets := store.secrets
	secretChanged := false
	if request.ClearAPIKey {
		secrets.APIKey = ""
		secretChanged = true
	} else if request.APIKey != nil {
		secrets.APIKey = *request.APIKey
		secretChanged = true
	}
	if request.ClearEmbeddingAPIKey {
		secrets.EmbeddingAPIKey = ""
		secretChanged = true
	} else if request.EmbeddingAPIKey != nil {
		secrets.EmbeddingAPIKey = *request.EmbeddingAPIKey
		secretChanged = true
	}
	if err := privatefile.WriteJSON(store.configPath, validated); err != nil {
		return managementapi.AgentConfigSaveResponse{}, fmt.Errorf("save Agent configuration: %w", err)
	}
	if secretChanged {
		if err := privatefile.WriteJSON(store.secretsPath, secrets); err != nil {
			return managementapi.AgentConfigSaveResponse{}, fmt.Errorf("save Agent secret: %w", err)
		}
	}
	store.config = validated
	store.configured = true
	store.secrets = secrets
	if !store.credentialOverrideByEnv {
		store.credentialConfigured = secrets.APIKey != ""
	}
	if !store.embeddingCredentialOverrideByEnv {
		store.embeddingCredentialConfigured = secrets.EmbeddingAPIKey != ""
	}
	return managementapi.AgentConfigSaveResponse{
		AgentConfigSnapshot: store.snapshotLocked(), RequiresRestart: true,
	}, nil
}

func (store *agentConfigStore) configForRuntime() agentdaemon.Config {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.config
}

func (store *agentConfigStore) loadEffectiveCredentials(config *configuration) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if config.agentConfig == "" {
		return nil
	}
	secrets := store.secrets
	if !config.agentTokenEnvSet {
		config.agentToken = secrets.AgentToken
	}
	if !config.agentAPIKeyEnvSet {
		config.agentAPIKey = secrets.APIKey
	}
	if !config.agentEmbeddingAPIKeyEnvSet {
		config.agentEmbeddingAPIKey = secrets.EmbeddingAPIKey
	}
	if config.agentToken == "" {
		token, err := newPrivateToken()
		if err != nil {
			return fmt.Errorf("create internal Agent token: %w", err)
		}
		config.agentToken = token
		secrets.AgentToken = token
		if err := privatefile.WriteJSON(store.secretsPath, secrets); err != nil {
			return fmt.Errorf("save internal Agent token: %w", err)
		}
		store.secrets = secrets
	}
	if len(config.agentToken) < 32 {
		return errors.New("RIN_AGENT_TOKEN or saved Agent token must contain at least 32 bytes")
	}
	if config.agentToken == config.token {
		return errors.New("RIN_AGENT_TOKEN must differ from RIN_CONTROL_TOKEN")
	}
	if config.agentAPIKey != "" && (config.agentAPIKey == config.agentToken || config.agentAPIKey == config.token) {
		return errors.New("RIN_AGENT_API_KEY must differ from daemon tokens")
	}
	if config.agentEmbeddingAPIKey != "" &&
		(config.agentEmbeddingAPIKey == config.agentToken || config.agentEmbeddingAPIKey == config.token) {
		return errors.New("RIN_AGENT_EMBEDDING_API_KEY must differ from daemon tokens")
	}
	store.credentialOverrideByEnv = config.agentAPIKeyEnvSet
	store.credentialConfigured = config.agentAPIKey != ""
	store.embeddingCredentialOverrideByEnv = config.agentEmbeddingAPIKeyEnvSet
	store.embeddingCredentialConfigured = config.agentEmbeddingAPIKey != ""
	return nil
}

func newPrivateToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

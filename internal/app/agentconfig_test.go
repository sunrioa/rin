package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/internal/privatefile"
	"github.com/sunrioa/rin/managementapi"
)

func TestAgentConfigStoreWritesValidatedAtomicPrivateFiles(t *testing.T) {
	dataDir := t.TempDir()
	store, err := openAgentConfigStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	config := validConsoleAgentConfig()
	key := "provider-secret-that-must-not-enter-config"
	response, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{
		Model: config.Model, Memory: &config.Memory, APIKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.RequiresRestart || !response.CredentialConfigured {
		t.Fatalf("save response = %#v", response)
	}
	configPath := managedAgentConfigPath(dataDir)
	secretPath := managedAgentSecretsPath(dataDir)
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(configBytes, []byte(key)) {
		t.Fatal("API key was written into Agent configuration")
	}
	if _, err := agentdaemon.LoadConfig(configPath); err != nil {
		t.Fatalf("saved configuration cannot be reopened: %v", err)
	}
	for _, path := range []string{configPath, secretPath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	var secrets agentSecrets
	if err := privatefile.ReadJSON(secretPath, maxAgentSecretBytes, &secrets); err != nil {
		t.Fatal(err)
	}
	if secrets.APIKey != key {
		t.Fatalf("saved secret = %q", secrets.APIKey)
	}
	before := append([]byte(nil), configBytes...)
	invalid := config.Model
	invalid.BaseURL = "http://remote.example.test/v1"
	if _, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{
		Model: invalid, Memory: &config.Memory,
	}); err == nil {
		t.Fatal("invalid remote plaintext model URL was accepted")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid save replaced the previous configuration")
	}
	cleared, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{
		Model: config.Model, Memory: &config.Memory, ClearAPIKey: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.CredentialConfigured {
		t.Fatal("cleared API key still reported as configured")
	}
	secrets = agentSecrets{}
	if err := privatefile.ReadJSON(secretPath, maxAgentSecretBytes, &secrets); err != nil {
		t.Fatal(err)
	}
	if secrets.APIKey != "" {
		t.Fatal("API key was not cleared from private secret file")
	}
}

func TestAgentLookaheadConfigRoundTripPreservesOmittedLimits(t *testing.T) {
	store, err := openAgentConfigStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.AgentConfig(context.Background())
	if err != nil || initial.Lookahead.Disabled || initial.Lookahead.MaxConcurrent != 2 {
		t.Fatalf("lookahead defaults: %#v %v", initial.Lookahead, err)
	}
	config := validConsoleAgentConfig()
	options := &cognition.LookaheadOptions{Disabled: true, MaxConcurrent: 3, TimeoutMillis: 500, DraftTTLMillis: 2000}
	response, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{Model: config.Model, Memory: &config.Memory, Lookahead: options})
	if err != nil || !response.RequiresRestart || !response.Lookahead.Disabled {
		t.Fatalf("lookahead save: %#v %v", response.Lookahead, err)
	}
	options.MaxConcurrent = 31
	response, err = store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{Model: config.Model, Memory: &config.Memory})
	if err != nil || response.Lookahead.MaxConcurrent != 3 || !response.Lookahead.Disabled {
		t.Fatalf("omitted lookahead update replaced options: %#v %v", response.Lookahead, err)
	}
	loaded, err := agentdaemon.LoadConfig(store.configPath)
	if err != nil || loaded.Runtime.Lookahead == nil || loaded.Runtime.Lookahead.TimeoutMillis != 500 {
		t.Fatalf("lookahead file roundtrip: %#v %v", loaded.Runtime.Lookahead, err)
	}
	if _, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{Model: config.Model, Memory: &config.Memory, Lookahead: &cognition.LookaheadOptions{TimeoutMillis: 50}}); !errors.Is(err, managementapi.ErrInvalidAgentConfig) {
		t.Fatalf("invalid lookahead bounds accepted: %v", err)
	}
}

func TestParseConfigurationAutoUsesSavedAgentConfig(t *testing.T) {
	dataDir := t.TempDir()
	path := managedAgentConfigPath(dataDir)
	if err := privatefile.WriteJSON(path, validConsoleAgentConfig()); err != nil {
		t.Fatal(err)
	}
	config, err := parseConfiguration(nil, testEnvironment(map[string]string{
		"RIN_CONTROL_TOKEN":     "0123456789abcdef0123456789abcdef",
		"RIN_CONTROL_PRINCIPAL": "player.one",
		"RIN_CONTROL_DATA_DIR":  dataDir,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.agentConfig != path || config.agentToken != "" {
		t.Fatalf("auto configuration = %#v", config)
	}
	store, err := openAgentConfigStore(dataDir, config.agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.loadEffectiveCredentials(&config); err != nil {
		t.Fatal(err)
	}
	if len(config.agentToken) < 32 {
		t.Fatalf("generated Agent token is too short: %d", len(config.agentToken))
	}
	if _, err := os.Stat(managedAgentSecretsPath(dataDir)); err != nil {
		t.Fatalf("generated secrets file is missing: %v", err)
	}
}

func TestAgentConfigEnvironmentKeyOverridesLocalSecret(t *testing.T) {
	dataDir := t.TempDir()
	store, err := openAgentConfigStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	config := validConsoleAgentConfig()
	localKey := "local-key"
	if _, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{
		Model: config.Model, Memory: &config.Memory, APIKey: &localKey,
	}); err != nil {
		t.Fatal(err)
	}
	runtimeConfig := configuration{
		agentConfig: managedAgentConfigPath(dataDir), token: "control-token",
		agentAPIKey: "environment-key", agentAPIKeyEnvSet: true,
	}
	if err := store.loadEffectiveCredentials(&runtimeConfig); err != nil {
		t.Fatal(err)
	}
	if runtimeConfig.agentAPIKey != "environment-key" || !store.credentialConfigured {
		t.Fatalf("environment override was not used: config=%#v store=%#v", runtimeConfig, store)
	}
}

func validConsoleAgentConfig() agentdaemon.Config {
	config := defaultAgentConfig()
	config.Model.BaseURL = "http://127.0.0.1:1/v1"
	config.Model.Model = "test-model"
	return config
}

func TestAgentConfigStoreRejectsAmbiguousCredentialMutation(t *testing.T) {
	store, err := openAgentConfigStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	key := "secret"
	config := validConsoleAgentConfig()
	_, err = store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{
		Model: config.Model, Memory: &config.Memory, APIKey: &key, ClearAPIKey: true,
	})
	if err == nil || !strings.Contains(err.Error(), "api_key and clear_api_key") {
		t.Fatalf("ambiguous credential mutation result = %v", err)
	}
	if !errors.Is(err, managementapi.ErrInvalidAgentConfig) {
		t.Fatalf("ambiguous credential mutation classification = %v", err)
	}
}

func TestAgentConfigJSONDoesNotContainSecretFieldInSnapshot(t *testing.T) {
	store, err := openAgentConfigStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	key := "secret-not-for-response"
	config := validConsoleAgentConfig()
	response, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{
		Model: config.Model, Memory: &config.Memory, APIKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(key)) || bytes.Contains(body, []byte(`"api_key"`)) {
		t.Fatalf("snapshot leaked secret: %s", body)
	}
}

func TestAgentConfigStoreUsesExpectedPaths(t *testing.T) {
	dataDir := t.TempDir()
	store, err := openAgentConfigStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(store.configPath) != filepath.Join(dataDir, "agent") ||
		filepath.Dir(store.secretsPath) != filepath.Join(dataDir, "agent") {
		t.Fatalf("store paths = %#v", store)
	}
}

func TestAgentConfigStorePersistsSemanticEmbeddingWithoutLeakingKey(t *testing.T) {
	dataDir := t.TempDir()
	store, err := openAgentConfigStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	config := validConsoleAgentConfig()
	config.Memory.SemanticEmbedding = agentdaemon.SemanticEmbeddingConfig{
		Enabled: true, Provider: agentdaemon.ProviderOpenAICompatible,
		BaseURL: "http://127.0.0.1:2/v1", Model: "embedding-test",
		Authentication: agentdaemon.AuthenticationBearerEnv,
		AllowedDomains: []cognition.MemoryDomain{
			cognition.MemoryCommonSemantic, cognition.MemoryActorSemantic,
		},
	}
	key := "embedding-secret-that-must-not-enter-config"
	response, err := store.SaveAgentConfig(context.Background(), managementapi.AgentConfigSaveRequest{
		Model: config.Model, Memory: &config.Memory, EmbeddingAPIKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.EmbeddingCredentialConfigured || !response.Memory.SemanticEmbedding.Enabled {
		t.Fatalf("embedding response = %#v", response)
	}
	configBytes, err := os.ReadFile(managedAgentConfigPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(configBytes, []byte(key)) {
		t.Fatal("Embedding API key was written into Agent configuration")
	}
	var secrets agentSecrets
	if err := privatefile.ReadJSON(managedAgentSecretsPath(dataDir), maxAgentSecretBytes, &secrets); err != nil {
		t.Fatal(err)
	}
	if secrets.EmbeddingAPIKey != key {
		t.Fatal("Embedding API key was not persisted in the private secret file")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(key)) || bytes.Contains(encoded, []byte(`"embedding_api_key"`)) {
		t.Fatalf("embedding response leaked secret: %s", encoded)
	}
}

// Package agentdaemon assembles Rin's engine-neutral internal Agent Runtime.
package agentdaemon

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/privatefile"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/provider/openai"
)

const (
	ConfigVersion                  = "rin.agent.config/v1"
	ProviderOpenAICompatible       = "openai-compatible"
	AuthenticationBearerEnv        = "bearer-env"
	AuthenticationNone             = "none"
	maxConfigBytes           int64 = 4 << 20
)

type Config struct {
	ContractVersion  string                     `json:"contract_version"`
	ClientPrincipal  host.Principal             `json:"client_principal"`
	RuntimePrincipal string                     `json:"runtime_principal_id,omitempty"`
	Model            ModelConfig                `json:"model"`
	Personas         []cognition.PersonaProfile `json:"personas"`
	PersonaBindings  []cognition.PersonaBinding `json:"persona_bindings"`
	Skills           []cognition.Skill          `json:"skills,omitempty"`
	Memory           MemoryConfig               `json:"memory,omitempty"`
	Tasks            TaskConfig                 `json:"tasks,omitempty"`
	Scheduler        SchedulerConfig            `json:"scheduler,omitempty"`
	Runtime          RuntimeConfig              `json:"runtime,omitempty"`
	Learning         LearningConfig             `json:"learning,omitempty"`
}

type ModelConfig struct {
	Provider             string           `json:"provider"`
	BaseURL              string           `json:"base_url"`
	Model                string           `json:"model"`
	ResponseFormat       string           `json:"response_format,omitempty"`
	Authentication       string           `json:"authentication,omitempty"`
	MaxContextCharacters uint32           `json:"max_context_characters,omitempty"`
	MaxOutputTokens      int              `json:"max_output_tokens,omitempty"`
	Temperature          float64          `json:"temperature,omitempty"`
	Resilience           ResilienceConfig `json:"resilience,omitempty"`
}

type ResilienceConfig struct {
	MaxAttempts          int    `json:"max_attempts,omitempty"`
	AttemptTimeoutMillis uint32 `json:"attempt_timeout_millis,omitempty"`
	TotalTimeoutMillis   uint32 `json:"total_timeout_millis,omitempty"`
	InitialBackoffMillis uint32 `json:"initial_backoff_millis,omitempty"`
	MaxBackoffMillis     uint32 `json:"max_backoff_millis,omitempty"`
	FailureThreshold     int    `json:"failure_threshold,omitempty"`
	OpenDurationMillis   uint32 `json:"open_duration_millis,omitempty"`
}

type MemoryConfig struct {
	MaxActiveRecordsPerNamespace uint32 `json:"max_active_records_per_namespace,omitempty"`
	MaxHistoryPerNamespace       uint32 `json:"max_history_per_namespace,omitempty"`
}

type TaskConfig struct {
	MaxTasks uint32 `json:"max_tasks,omitempty"`
}

type SchedulerConfig struct {
	WorkerCount             uint32 `json:"worker_count,omitempty"`
	QueueCapacity           uint32 `json:"queue_capacity,omitempty"`
	ReconcileIntervalMillis uint32 `json:"reconcile_interval_millis,omitempty"`
}

type RuntimeConfig struct {
	ControllerLeaseMillis uint32 `json:"controller_lease_millis,omitempty"`
	RenewBeforeMillis     uint32 `json:"renew_before_millis,omitempty"`
	OperationWaitMillis   uint32 `json:"operation_wait_millis,omitempty"`
	MaxAdvancesPerRun     uint32 `json:"max_advances_per_run,omitempty"`
	MemoryMaxRecords      uint32 `json:"memory_max_records,omitempty"`
	MemoryMaxCharacters   uint32 `json:"memory_max_characters,omitempty"`
}

type LearningConfig struct {
	Enabled         bool   `json:"enabled,omitempty"`
	PublishMode     string `json:"publish_mode,omitempty"`
	MinActions      uint32 `json:"min_actions,omitempty"`
	Adapter         string `json:"adapter,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

// LoadConfig reads one strict, private configuration. Credentials are not a
// member of Config and therefore fail unknown-field validation if embedded.
func LoadConfig(path string) (Config, error) {
	var config Config
	if err := privatefile.ReadJSON(path, maxConfigBytes, &config); err != nil {
		return Config{}, fmt.Errorf("load Agent configuration: %w", err)
	}
	sealed, err := normalizeConfig(config)
	if err != nil {
		return Config{}, fmt.Errorf("validate Agent configuration: %w", err)
	}
	return sealed, nil
}

func normalizeConfig(config Config) (Config, error) {
	if config.ContractVersion != ConfigVersion {
		return Config{}, fmt.Errorf("contract_version must equal %s", ConfigVersion)
	}
	if len(config.Personas) == 0 || len(config.Personas) > 256 ||
		len(config.PersonaBindings) == 0 || len(config.PersonaBindings) > 256 {
		return Config{}, errors.New("personas and persona_bindings must each contain between 1 and 256 values")
	}
	if len(config.Skills) > 256 {
		return Config{}, errors.New("skills must contain at most 256 values")
	}

	config.ClientPrincipal.ID = strings.TrimSpace(config.ClientPrincipal.ID)
	if config.ClientPrincipal.ID == "" && len(config.ClientPrincipal.GrantedScopes) == 0 {
		config.ClientPrincipal.ID = "rin.agent-client"
	}
	if len(config.ClientPrincipal.GrantedScopes) == 0 {
		config.ClientPrincipal.GrantedScopes = []string{
			agentapi.ScopeTaskRead,
			agentapi.ScopeTaskExecute,
			agentapi.ScopeTaskCancel,
		}
	} else {
		config.ClientPrincipal.GrantedScopes = append(
			[]string(nil), config.ClientPrincipal.GrantedScopes...,
		)
	}
	if err := validateTaskPrincipal(config.ClientPrincipal); err != nil {
		return Config{}, err
	}
	config.RuntimePrincipal = strings.TrimSpace(config.RuntimePrincipal)
	if config.RuntimePrincipal == "" {
		config.RuntimePrincipal = "rin.internal"
	}
	if err := host.ValidatePrincipal(host.Principal{
		ID: config.RuntimePrincipal, GrantedScopes: []string{"host.admin"},
	}); err != nil {
		return Config{}, fmt.Errorf("runtime_principal_id: %w", err)
	}

	if _, err := cognition.NewLocalPersonaProvider(
		config.Personas, config.PersonaBindings,
	); err != nil {
		return Config{}, fmt.Errorf("persona configuration: %w", err)
	}
	if _, err := cognition.NewLocalSkillProvider(config.Skills); err != nil {
		return Config{}, fmt.Errorf("skill configuration: %w", err)
	}
	if _, err := cognition.NewLocalMemoryProvider(config.memoryProviderConfig()); err != nil {
		return Config{}, fmt.Errorf("memory configuration: %w", err)
	}
	if _, err := cognition.NewLocalTaskStore(config.Tasks.MaxTasks); err != nil {
		return Config{}, fmt.Errorf("task configuration: %w", err)
	}
	if err := normalizeModelConfig(&config.Model); err != nil {
		return Config{}, err
	}
	if err := validateSchedulerConfig(config.Scheduler); err != nil {
		return Config{}, err
	}
	if err := validateRuntimeConfig(config.Runtime); err != nil {
		return Config{}, err
	}
	if err := normalizeLearningConfig(&config.Learning); err != nil {
		return Config{}, err
	}
	return config, nil
}

func normalizeLearningConfig(config *LearningConfig) error {
	config.PublishMode = strings.TrimSpace(config.PublishMode)
	config.Adapter = strings.TrimSpace(config.Adapter)
	if !config.Enabled {
		if config.PublishMode != "" || config.MinActions != 0 || config.Adapter != "" ||
			config.MaxOutputTokens != 0 {
			return errors.New("learning settings require learning.enabled=true")
		}
		return nil
	}
	if config.PublishMode == "" {
		config.PublishMode = string(cognition.SkillPublishDraft)
	}
	if config.PublishMode != string(cognition.SkillPublishDraft) &&
		config.PublishMode != string(cognition.SkillPublishLearned) {
		return errors.New("learning.publish_mode must be draft or learned")
	}
	if config.MinActions == 0 {
		config.MinActions = 3
	}
	if config.MinActions > 100 {
		return errors.New("learning.min_actions must not exceed 100")
	}
	if config.Adapter != "" {
		if _, err := cognition.NewLocalSkillProvider([]cognition.Skill{{
			SkillSummary: cognition.SkillSummary{
				SkillID: "validation.skill", Version: "v1", Summary: "Validation",
				Source: "system", Adapters: []string{config.Adapter},
			},
			Instructions: "Validation only.",
		}}); err != nil {
			return fmt.Errorf("learning.adapter: %w", err)
		}
	}
	if config.MaxOutputTokens == 0 {
		config.MaxOutputTokens = 1_200
	}
	if config.MaxOutputTokens < 128 || config.MaxOutputTokens > 4_096 {
		return errors.New("learning.max_output_tokens must be between 128 and 4096")
	}
	return nil
}

func validateTaskPrincipal(principal host.Principal) error {
	if err := host.ValidatePrincipal(principal); err != nil {
		return fmt.Errorf("client_principal: %w", err)
	}
	allowed := map[string]struct{}{
		agentapi.ScopeTaskRead: {}, agentapi.ScopeTaskExecute: {}, agentapi.ScopeTaskCancel: {},
	}
	for _, scope := range principal.GrantedScopes {
		if _, exists := allowed[scope]; !exists {
			return fmt.Errorf("client_principal scope %q is not a Task API scope", scope)
		}
	}
	return nil
}

func normalizeModelConfig(config *ModelConfig) error {
	config.Provider = strings.TrimSpace(config.Provider)
	if config.Provider == "" {
		config.Provider = ProviderOpenAICompatible
	}
	if config.Provider != ProviderOpenAICompatible {
		return fmt.Errorf("model.provider must equal %s", ProviderOpenAICompatible)
	}
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.Model = strings.TrimSpace(config.Model)
	if config.ResponseFormat == "" {
		config.ResponseFormat = "json_schema"
	}
	if config.Authentication == "" {
		config.Authentication = AuthenticationBearerEnv
	}
	if config.Authentication != AuthenticationBearerEnv && config.Authentication != AuthenticationNone {
		return errors.New("model.authentication must be bearer-env or none")
	}
	if err := validateModelTransport(*config); err != nil {
		return err
	}
	client, err := openai.New(openai.Config{
		BaseURL: config.BaseURL, Model: config.Model, ResponseFormat: config.ResponseFormat,
	})
	if err != nil {
		return fmt.Errorf("model: %w", err)
	}
	resilient, err := provider.NewResilient(client, config.Resilience.providerConfig())
	if err != nil {
		return fmt.Errorf("model.resilience: %w", err)
	}
	decision := cognition.StructuredDecisionProvider{
		GenerationProvider:   resilient,
		MaxContextCharacters: config.MaxContextCharacters,
		MaxOutputTokens:      config.MaxOutputTokens,
		Temperature:          config.Temperature,
	}
	if err := decision.Validate(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	return validateResilienceBounds(config.Resilience)
}

func validateModelTransport(config ModelConfig) error {
	parsed, err := url.Parse(config.BaseURL)
	if err != nil {
		return errors.New("model.base_url is invalid")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	hostname := parsed.Hostname()
	if strings.EqualFold(hostname, "localhost") {
		return nil
	}
	address := net.ParseIP(hostname)
	if address != nil && address.IsLoopback() {
		return nil
	}
	return errors.New("model.base_url must use HTTPS unless it targets loopback")
}

func validateResilienceBounds(config ResilienceConfig) error {
	if config.MaxAttempts < 0 || config.FailureThreshold < 0 ||
		config.AttemptTimeoutMillis > 120_000 || config.TotalTimeoutMillis > 300_000 ||
		config.InitialBackoffMillis > 60_000 || config.MaxBackoffMillis > 60_000 ||
		config.OpenDurationMillis > 600_000 || config.FailureThreshold > 100 {
		return errors.New("model.resilience exceeds daemon safety bounds")
	}
	return nil
}

func validateSchedulerConfig(config SchedulerConfig) error {
	if config.WorkerCount > 64 || config.QueueCapacity > 100_000 {
		return errors.New("scheduler worker count or queue capacity is too large")
	}
	if config.ReconcileIntervalMillis != 0 &&
		(config.ReconcileIntervalMillis < 50 || config.ReconcileIntervalMillis > 60_000) {
		return errors.New("scheduler reconcile interval must be between 50 and 60000 milliseconds")
	}
	return nil
}

func validateRuntimeConfig(config RuntimeConfig) error {
	lease := config.ControllerLeaseMillis
	if lease != 0 && (lease < 5_000 || lease > 300_000) {
		return errors.New("runtime controller lease must be between 5000 and 300000 milliseconds")
	}
	effectiveLease := lease
	if effectiveLease == 0 {
		effectiveLease = 60_000
	}
	if config.RenewBeforeMillis >= effectiveLease && config.RenewBeforeMillis != 0 {
		return errors.New("runtime renewal window must be shorter than the controller lease")
	}
	if config.OperationWaitMillis > 25_000 || config.MaxAdvancesPerRun > 1_024 ||
		config.MemoryMaxRecords > 128 || config.MemoryMaxCharacters > 64_000 {
		return errors.New("runtime execution or memory budget exceeds its safety bound")
	}
	return nil
}

func (config Config) memoryProviderConfig() cognition.LocalMemoryConfig {
	return cognition.LocalMemoryConfig{
		MaxActiveRecordsPerNamespace: config.Memory.MaxActiveRecordsPerNamespace,
		MaxHistoryPerNamespace:       config.Memory.MaxHistoryPerNamespace,
	}
}

func (config ResilienceConfig) providerConfig() provider.ResilienceConfig {
	return provider.ResilienceConfig{
		MaxAttempts:      config.MaxAttempts,
		AttemptTimeout:   time.Duration(config.AttemptTimeoutMillis) * time.Millisecond,
		TotalTimeout:     time.Duration(config.TotalTimeoutMillis) * time.Millisecond,
		InitialBackoff:   time.Duration(config.InitialBackoffMillis) * time.Millisecond,
		MaxBackoff:       time.Duration(config.MaxBackoffMillis) * time.Millisecond,
		FailureThreshold: config.FailureThreshold,
		OpenDuration:     time.Duration(config.OpenDurationMillis) * time.Millisecond,
	}
}

func (config SchedulerConfig) reconcileInterval() time.Duration {
	return time.Duration(config.ReconcileIntervalMillis) * time.Millisecond
}

package agentdaemon

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/provider/openai"
)

type Options struct {
	Config             Config
	DataDir            string
	Control            *controlplane.Service
	HTTPToken          string
	APIKey             string
	GenerationProvider provider.StructuredGenerationProvider
	Skills             cognition.SkillProvider
}

type Daemon struct {
	handler   http.Handler
	service   *agentapi.Service
	tasks     *cognition.FileTaskStore
	memory    *cognition.FileMemoryProvider
	decisions *cognition.FileDecisionRecorder

	closeOnce sync.Once
	closeErr  error
}

// Open creates the internal runtime without performing a model request.
func Open(options Options) (*Daemon, error) {
	if options.Control == nil {
		return nil, errors.New("Control Plane service is required")
	}
	if options.DataDir == "" {
		return nil, errors.New("Control Plane data directory is required")
	}
	config, err := normalizeConfig(options.Config)
	if err != nil {
		return nil, fmt.Errorf("validate Agent configuration: %w", err)
	}
	generation, err := buildGenerationProvider(config.Model, options.APIKey, options.GenerationProvider)
	if err != nil {
		return nil, err
	}
	model := cognition.StructuredDecisionProvider{
		GenerationProvider:   generation,
		MaxContextCharacters: config.Model.MaxContextCharacters,
		MaxOutputTokens:      config.Model.MaxOutputTokens,
		Temperature:          config.Model.Temperature,
	}
	if err := model.Validate(); err != nil {
		return nil, fmt.Errorf("validate decision provider: %w", err)
	}
	personas, err := cognition.NewLocalPersonaProvider(config.Personas, config.PersonaBindings)
	if err != nil {
		return nil, err
	}
	skills := options.Skills
	if skills == nil {
		catalog, _, catalogErr := cognition.OpenDefaultSkillCatalog(options.DataDir, config.Skills)
		err = catalogErr
		if err != nil {
			return nil, fmt.Errorf("open skill catalog: %w", err)
		}
		skills = catalog
	}
	stateDirectory := filepath.Join(options.DataDir, "agent")
	memory, err := cognition.OpenFileMemoryProvider(
		filepath.Join(stateDirectory, "memory.json"), config.memoryProviderConfig(),
	)
	if err != nil {
		return nil, fmt.Errorf("open Agent memory: %w", err)
	}
	tasks, err := cognition.OpenFileTaskStore(
		filepath.Join(stateDirectory, "tasks.json"), config.Tasks.MaxTasks,
	)
	if err != nil {
		_ = memory.Close()
		return nil, fmt.Errorf("open Agent tasks: %w", err)
	}
	decisions, err := cognition.OpenFileDecisionRecorder(
		filepath.Join(stateDirectory, "decision-records.json"), cognition.DefaultDecisionRecordLimit,
	)
	if err != nil {
		_ = tasks.Close()
		_ = memory.Close()
		return nil, fmt.Errorf("open Agent decision records: %w", err)
	}
	cleanupStores := func(base error) error {
		return errors.Join(base, decisions.Close(), tasks.Close(), memory.Close())
	}
	runtimePrincipal := host.Principal{
		ID: config.RuntimePrincipal, GrantedScopes: []string{controlplane.ScopeHostAdmin},
	}
	environment, err := cognition.NewControlEnvironment(options.Control, runtimePrincipal)
	if err != nil {
		return nil, cleanupStores(err)
	}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: runtimePrincipal, Control: options.Control, Environment: environment,
		Persona: personas, Memory: memory, Skills: skills, Model: model, Tasks: tasks,
		Decisions:             decisions,
		ControllerLeaseMillis: config.Runtime.ControllerLeaseMillis,
		RenewBeforeMillis:     config.Runtime.RenewBeforeMillis,
		OperationWaitMillis:   config.Runtime.OperationWaitMillis,
		MaxAdvancesPerRun:     config.Runtime.MaxAdvancesPerRun,
		MemoryBudget: cognition.MemoryBudget{
			MaxRecords:    config.Runtime.MemoryMaxRecords,
			MaxCharacters: config.Runtime.MemoryMaxCharacters,
		},
	})
	if err != nil {
		return nil, cleanupStores(err)
	}
	service, err := agentapi.New(agentapi.Options{
		Runtime:           runtime,
		WorkerCount:       config.Scheduler.WorkerCount,
		QueueCapacity:     config.Scheduler.QueueCapacity,
		ReconcileInterval: config.Scheduler.reconcileInterval(),
	})
	if err != nil {
		return nil, cleanupStores(err)
	}
	handler, err := agentapi.NewHTTPHandler(service, agentapi.HTTPOptions{
		Token: options.HTTPToken, ClientPrincipal: config.ClientPrincipal,
	})
	if err != nil {
		service.Close()
		return nil, cleanupStores(err)
	}
	return &Daemon{
		handler: handler, service: service, tasks: tasks, memory: memory, decisions: decisions,
	}, nil
}

func buildGenerationProvider(
	config ModelConfig,
	apiKey string,
	injected provider.StructuredGenerationProvider,
) (provider.StructuredGenerationProvider, error) {
	if config.Authentication == AuthenticationNone && apiKey != "" {
		return nil, errors.New("RIN_AGENT_API_KEY must be unset when model.authentication=none")
	}
	base := injected
	if base == nil {
		switch config.Authentication {
		case AuthenticationBearerEnv:
			if apiKey == "" {
				return nil, errors.New("RIN_AGENT_API_KEY is required by model.authentication=bearer-env")
			}
		case AuthenticationNone:
		default:
			return nil, errors.New("unsupported model authentication")
		}
		client, err := openai.New(openai.Config{
			BaseURL: config.BaseURL, APIKey: apiKey, Model: config.Model,
			ResponseFormat: config.ResponseFormat,
		})
		if err != nil {
			return nil, fmt.Errorf("create generation provider: %w", err)
		}
		base = client
	}
	resilient, err := provider.NewResilient(base, config.Resilience.providerConfig())
	if err != nil {
		return nil, fmt.Errorf("create resilient generation provider: %w", err)
	}
	return resilient, nil
}

func (daemon *Daemon) Handler() http.Handler {
	return daemon.handler
}

// Close stops task workers before releasing persistent task and memory locks.
func (daemon *Daemon) Close() error {
	if daemon == nil {
		return nil
	}
	daemon.closeOnce.Do(func() {
		daemon.service.Close()
		daemon.closeErr = errors.Join(
			daemon.decisions.Close(), daemon.tasks.Close(), daemon.memory.Close(),
		)
	})
	return daemon.closeErr
}

package agentdaemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/experience"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/provider/openai"
	"github.com/sunrioa/rin/signalbox"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

type Options struct {
	Config                    Config
	DataDir                   string
	Control                   *controlplane.Service
	HTTPToken                 string
	APIKey                    string
	EmbeddingAPIKey           string
	GenerationProvider        provider.StructuredGenerationProvider
	EmbeddingProvider         provider.EmbeddingProvider
	Skills                    cognition.SkillProvider
	LearnedSkills             cognition.SkillWriter
	Personas                  cognition.PersonaProvider
	Memory                    cognition.MemoryProvider
	OutcomesRecordedByControl bool
	PlanStore                 *taskstate.Store
	Signals                   *signalbox.Store
}

type Daemon struct {
	handler      http.Handler
	service      *agentapi.Service
	runtime      *cognition.AgentRuntime
	taskClient   *agentapi.ClientService
	tasks        *cognition.SQLiteTaskStore
	memory       cognition.MemoryProvider
	memoryCloser interface{ Close() error }
	decisions    *cognition.SQLiteDecisionRecorder
	signalCancel context.CancelFunc
	signalWG     sync.WaitGroup

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
	_, preparedSemanticMemory := options.Memory.(*cognition.SemanticMemoryProvider)
	var embedder provider.EmbeddingProvider
	if preparedSemanticMemory {
		if !config.Memory.SemanticEmbedding.Enabled {
			return nil, errors.New("injected semantic memory requires memory.semantic_embedding.enabled=true")
		}
		if options.EmbeddingAPIKey != "" || options.EmbeddingProvider != nil {
			return nil, errors.New("prepared semantic memory cannot also configure an embedding provider")
		}
	} else {
		embedder, err = buildEmbeddingProvider(
			config.Memory.SemanticEmbedding, options.EmbeddingAPIKey, options.EmbeddingProvider,
		)
		if err != nil {
			return nil, err
		}
	}
	personas := options.Personas
	if personas == nil {
		personas, err = cognition.NewLocalPersonaProvider(config.Personas, config.PersonaBindings)
		if err != nil {
			return nil, err
		}
	}
	skills := options.Skills
	learnedSkills := options.LearnedSkills
	if skills == nil {
		catalog, learned, catalogErr := cognition.OpenDefaultSkillCatalog(options.DataDir, config.Skills)
		err = catalogErr
		if err != nil {
			return nil, fmt.Errorf("open skill catalog: %w", err)
		}
		skills = catalog
		learnedSkills = learned
	}
	var learning *cognition.SkillLearningOptions
	if config.Learning.Enabled {
		drafts, openErr := cognition.OpenDirectorySkillProvider(
			filepath.Join(options.DataDir, "skills", "drafts"), "draft", true,
		)
		if openErr != nil {
			return nil, fmt.Errorf("open skill drafts: %w", openErr)
		}
		learning = &cognition.SkillLearningOptions{
			Generator: experience.ModelDraftGenerator{
				Provider: generation, MaxTokens: config.Learning.MaxOutputTokens,
			},
			Drafts: drafts, Learned: learnedSkills,
			Mode:       cognition.SkillPublishMode(config.Learning.PublishMode),
			MinActions: config.Learning.MinActions, Adapter: config.Learning.Adapter,
		}
	}
	stateDirectory := filepath.Join(options.DataDir, "agent")
	memory := options.Memory
	var memoryCloser interface{ Close() error }
	var sqliteMemory *cognition.SQLiteMemoryProvider
	ownedSQLite := false
	if memory == nil {
		var openErr error
		sqliteMemory, openErr = cognition.OpenSQLiteMemoryProvider(
			filepath.Join(stateDirectory, "memory.db"), config.MemoryProviderConfig(),
		)
		if openErr != nil {
			return nil, fmt.Errorf("open Agent memory: %w", openErr)
		}
		memory = sqliteMemory
		ownedSQLite = true
	} else if config.Memory.SemanticEmbedding.Enabled && !preparedSemanticMemory {
		var ok bool
		sqliteMemory, ok = memory.(*cognition.SQLiteMemoryProvider)
		if !ok {
			return nil, errors.New("semantic memory requires an injected SQLite memory provider")
		}
	}
	if config.Memory.SemanticEmbedding.Enabled && !preparedSemanticMemory {
		semantic, semanticErr := cognition.NewSemanticMemoryProvider(
			sqliteMemory, embedder, config.Memory.SemanticEmbedding.semanticMemoryConfig(),
		)
		if semanticErr != nil {
			if ownedSQLite {
				_ = sqliteMemory.Close()
			}
			return nil, fmt.Errorf("open semantic memory: %w", semanticErr)
		}
		memory = semantic
		memoryCloser = semantic
		if ownedSQLite {
			memoryCloser = closeFunc(func() error {
				return errors.Join(semantic.Close(), sqliteMemory.Close())
			})
		}
	} else if ownedSQLite {
		memoryCloser = sqliteMemory
	}
	tasks, err := cognition.OpenSQLiteTaskStore(
		filepath.Join(stateDirectory, "tasks.db"), config.Tasks.MaxTasks,
	)
	if err != nil {
		if memoryCloser != nil {
			_ = memoryCloser.Close()
		}
		return nil, fmt.Errorf("open Agent tasks: %w", err)
	}
	decisions, err := cognition.OpenSQLiteDecisionRecorder(
		filepath.Join(stateDirectory, "decision-records.db"), cognition.DefaultDecisionRecordLimit,
	)
	if err != nil {
		_ = tasks.Close()
		if memoryCloser != nil {
			_ = memoryCloser.Close()
		}
		return nil, fmt.Errorf("open Agent decision records: %w", err)
	}
	cleanupStores := func(base error) error {
		var memoryErr error
		if memoryCloser != nil {
			memoryErr = memoryCloser.Close()
		}
		return errors.Join(base, decisions.Close(), tasks.Close(), memoryErr)
	}
	runtimePrincipal := buildRuntimePrincipal(config.RuntimePrincipal)
	var plans taskstate.PlanClient
	if options.PlanStore != nil {
		planControl, planErr := controlplane.NewClientService(options.Control, runtimePrincipal)
		if planErr != nil {
			return nil, cleanupStores(planErr)
		}
		plans, planErr = taskstate.NewCoordinator(options.PlanStore, planControl)
		if planErr != nil {
			return nil, cleanupStores(planErr)
		}
	}
	environment, err := cognition.NewControlEnvironment(options.Control, runtimePrincipal)
	if err != nil {
		return nil, cleanupStores(err)
	}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: runtimePrincipal, Control: options.Control, Environment: environment,
		Persona: personas, Memory: memory, Skills: skills, Model: model, Tasks: tasks,
		Decisions:                 decisions,
		Learning:                  learning,
		Plans:                     plans,
		OutcomesRecordedByControl: options.OutcomesRecordedByControl,
		ControllerLeaseMillis:     config.Runtime.ControllerLeaseMillis,
		RenewBeforeMillis:         config.Runtime.RenewBeforeMillis,
		OperationWaitMillis:       config.Runtime.OperationWaitMillis,
		MaxAdvancesPerRun:         config.Runtime.MaxAdvancesPerRun,
		Lookahead:                 config.Runtime.Lookahead,
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
		runtime.Close()
		return nil, cleanupStores(err)
	}
	taskClient, err := agentapi.NewClientService(service, config.ClientPrincipal)
	if err != nil {
		service.Close()
		runtime.Close()
		return nil, cleanupStores(err)
	}
	handler, err := agentapi.NewHTTPHandler(service, agentapi.HTTPOptions{
		Token: options.HTTPToken, ClientPrincipal: config.ClientPrincipal,
	})
	if err != nil {
		service.Close()
		runtime.Close()
		return nil, cleanupStores(err)
	}
	daemon := &Daemon{
		handler: handler, service: service, taskClient: taskClient, tasks: tasks, memory: memory,
		runtime:      runtime,
		memoryCloser: memoryCloser, decisions: decisions,
	}
	if options.Signals != nil {
		signalContext, cancel := context.WithCancel(context.Background())
		daemon.signalCancel = cancel
		daemon.signalWG.Add(1)
		go daemon.runSignalScheduler(
			signalContext, options.Signals, options.Control, personas, runtimePrincipal,
		)
	}
	return daemon, nil
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
			ResponseFormat: config.ResponseFormat, ThinkingMode: config.ThinkingMode,
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

func buildEmbeddingProvider(
	config SemanticEmbeddingConfig,
	apiKey string,
	injected provider.EmbeddingProvider,
) (provider.EmbeddingProvider, error) {
	if !config.Enabled {
		if apiKey != "" || injected != nil {
			return nil, errors.New("embedding provider settings require memory.semantic_embedding.enabled=true")
		}
		return nil, nil
	}
	if injected != nil {
		if apiKey != "" {
			return nil, errors.New("RIN_AGENT_EMBEDDING_API_KEY must be unset with an injected embedding provider")
		}
		return injected, nil
	}
	switch config.Authentication {
	case AuthenticationBearerEnv:
		if apiKey == "" {
			return nil, errors.New("RIN_AGENT_EMBEDDING_API_KEY is required by semantic embedding authentication=bearer-env")
		}
	case AuthenticationNone:
		if apiKey != "" {
			return nil, errors.New("RIN_AGENT_EMBEDDING_API_KEY must be unset when semantic embedding authentication=none")
		}
	default:
		return nil, errors.New("unsupported semantic embedding authentication")
	}
	client, err := openai.NewEmbeddingClient(openai.EmbeddingConfig{
		BaseURL: config.BaseURL, APIKey: apiKey, Model: config.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("create embedding provider: %w", err)
	}
	return client, nil
}

// ConfigureSemanticMemory wraps the shared SQLite provider only when the
// optional semantic path is explicitly enabled. The returned closer owns the
// wrapper workers, never the caller-owned SQLite provider.
func ConfigureSemanticMemory(
	config SemanticEmbeddingConfig,
	local *cognition.SQLiteMemoryProvider,
	apiKey string,
	injected provider.EmbeddingProvider,
) (cognition.MemoryProvider, interface{ Close() error }, error) {
	if local == nil {
		return nil, nil, errors.New("SQLite memory provider is required")
	}
	if err := normalizeSemanticEmbeddingConfig(&config); err != nil {
		return nil, nil, err
	}
	embedder, err := buildEmbeddingProvider(config, apiKey, injected)
	if err != nil {
		return nil, nil, err
	}
	if !config.Enabled {
		return local, nil, nil
	}
	semantic, err := cognition.NewSemanticMemoryProvider(
		local, embedder, config.semanticMemoryConfig(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open semantic memory: %w", err)
	}
	return semantic, semantic, nil
}

type closeFunc func() error

func (function closeFunc) Close() error { return function() }

func (daemon *Daemon) Handler() http.Handler {
	return daemon.handler
}

func (daemon *Daemon) SnapshotTasks(ctx context.Context) (cognition.TaskSnapshot, error) {
	return daemon.tasks.Snapshot(ctx)
}

func (daemon *Daemon) ArchivedTasks(ctx context.Context, limit uint32) (cognition.TaskSnapshot, error) {
	return daemon.tasks.ArchivedTasks(ctx, limit)
}

func (daemon *Daemon) StartTask(
	ctx context.Context,
	input cognition.StartTaskInput,
) (cognition.TaskSession, error) {
	dispatch, err := daemon.taskClient.StartTask(ctx, input)
	return dispatch.Task, err
}

func (daemon *Daemon) GetTask(ctx context.Context, taskID string) (cognition.TaskSession, error) {
	return daemon.taskClient.GetTask(ctx, taskID)
}

func (daemon *Daemon) GetTaskTimeline(ctx context.Context, query timeline.Query) (timeline.Page, error) {
	return daemon.taskClient.GetTaskTimeline(ctx, query)
}

func (daemon *Daemon) RunTask(ctx context.Context, taskID string) (cognition.TaskSession, error) {
	dispatch, err := daemon.taskClient.RunTask(ctx, taskID)
	return dispatch.Task, err
}

func (daemon *Daemon) ResumeTask(ctx context.Context, taskID string) (cognition.TaskSession, error) {
	dispatch, err := daemon.taskClient.ResumeTask(ctx, taskID)
	return dispatch.Task, err
}

func (daemon *Daemon) CancelTask(ctx context.Context, taskID string) (cognition.TaskSession, error) {
	dispatch, err := daemon.taskClient.CancelTask(ctx, taskID)
	return dispatch.Task, err
}

// Close stops task workers before releasing persistent task and memory locks.
func (daemon *Daemon) Close() error {
	if daemon == nil {
		return nil
	}
	daemon.closeOnce.Do(func() {
		if daemon.signalCancel != nil {
			daemon.signalCancel()
			daemon.signalWG.Wait()
		}
		daemon.service.Close()
		daemon.runtime.Close()
		var memoryErr error
		if daemon.memoryCloser != nil {
			memoryErr = daemon.memoryCloser.Close()
		}
		daemon.closeErr = errors.Join(
			daemon.decisions.Close(), daemon.tasks.Close(), memoryErr,
		)
	})
	return daemon.closeErr
}

func (daemon *Daemon) ConfirmTaskCompletion(ctx context.Context, taskID string, revision uint64) (cognition.TaskSession, error) {
	dispatch, err := daemon.taskClient.ConfirmTaskCompletion(ctx, agentapi.CompletionConfirmationInput{TaskID: taskID, ExpectedRevision: revision})
	return dispatch.Task, err
}

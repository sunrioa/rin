// Package app assembles and serves the local Rin application.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/consoleui"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/managementapi"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/signalbox"
	"github.com/sunrioa/rin/skillapi"
	"github.com/sunrioa/rin/taskstate"
)

const shutdownTimeout = 5 * time.Second
const maxPolicyBytes int64 = 1 << 20

type configuration struct {
	address              string
	dataDir              string
	token                string
	principal            host.Principal
	policy               string
	agentConfig          string
	agentToken           string
	agentAPIKey          string
	agentEmbeddingAPIKey string
}

// Run parses the Control Daemon configuration, starts the local service, and
// shuts it down when ctx is canceled or the HTTP server exits.
func Run(
	ctx context.Context,
	arguments []string,
	lookupEnv func(string) (string, bool),
	stderr io.Writer,
) (result error) {
	config, err := parseConfiguration(arguments, lookupEnv, stderr)
	if err != nil {
		return err
	}
	policyEngine, err := loadPolicyEngine(config.policy)
	if err != nil {
		return err
	}
	var agentConfig agentdaemon.Config
	if config.agentConfig != "" {
		agentConfig, err = agentdaemon.LoadConfig(config.agentConfig)
		if err != nil {
			return err
		}
	}
	memory, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(config.dataDir, "agent", "memory.db"),
		agentConfig.MemoryProviderConfig(),
	)
	if err != nil {
		return fmt.Errorf("open shared memory: %w", err)
	}
	defer func() {
		result = errors.Join(result, memory.Close())
	}()
	sharedMemory, semanticCloser, err := agentdaemon.ConfigureSemanticMemory(
		agentConfig.Memory.SemanticEmbedding, memory, config.agentEmbeddingAPIKey, nil,
	)
	if err != nil {
		return fmt.Errorf("configure shared semantic memory: %w", err)
	}
	if semanticCloser != nil {
		defer func() {
			result = errors.Join(result, semanticCloser.Close())
		}()
	}
	personas, err := cognition.OpenFilePersonaStore(
		filepath.Join(config.dataDir, "agent", "personas.json"),
		cognition.PersonaSnapshot{
			Revision: 1, Profiles: agentConfig.Personas, Bindings: agentConfig.PersonaBindings,
		},
	)
	if err != nil {
		return fmt.Errorf("open shared personas: %w", err)
	}
	defer func() {
		result = errors.Join(result, personas.Close())
	}()
	outcomeSink, err := cognition.NewOutcomeMemorySink(sharedMemory)
	if err != nil {
		return fmt.Errorf("create Outcome memory projection: %w", err)
	}
	planStore, err := taskstate.OpenSQLiteStore(
		filepath.Join(config.dataDir, "agent", "taskstate.db"), taskstate.StoreConfig{},
	)
	if err != nil {
		return fmt.Errorf("open task plan store: %w", err)
	}
	defer func() {
		result = errors.Join(result, planStore.Close())
	}()
	planOutcomeSink, err := taskstate.NewOutcomeSink(planStore)
	if err != nil {
		return fmt.Errorf("create task plan Outcome projection: %w", err)
	}
	service, err := controlplane.OpenFile(
		config.dataDir,
		controlplane.Options{
			PolicyEngine: policyEngine,
			OutcomeSink:  controlplane.JoinOutcomeSinks(outcomeSink, planOutcomeSink),
		},
	)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, service.Close())
	}()
	planControlClient, err := controlplane.NewClientService(service, config.principal)
	if err != nil {
		return err
	}
	planCoordinator, err := taskstate.NewCoordinator(planStore, planControlClient)
	if err != nil {
		return err
	}
	planHandler, err := taskstate.NewHTTPHandler(
		planCoordinator, taskstate.HTTPOptions{Token: config.token},
	)
	if err != nil {
		return err
	}
	signals, err := signalbox.NewStore(signalbox.StoreConfig{})
	if err != nil {
		return err
	}
	defer signals.Close()
	signalService, err := signalbox.NewService(signals, service, planControlClient)
	if err != nil {
		return err
	}
	signalHandler, err := signalbox.NewHTTPHandler(
		signalService, signalbox.HTTPOptions{Token: config.token},
	)
	if err != nil {
		return err
	}
	handler, err := controlplane.NewHTTPHandler(
		service,
		controlplane.HTTPOptions{
			Token:           config.token,
			ClientPrincipal: &config.principal,
		},
	)
	if err != nil {
		return err
	}
	catalog, learnedSkills, err := cognition.OpenDefaultSkillCatalog(
		config.dataDir, agentConfig.Skills,
	)
	if err != nil {
		return fmt.Errorf("open shared skill catalog: %w", err)
	}
	skillService, err := skillapi.New(catalog, learnedSkills)
	if err != nil {
		return err
	}
	skillHandler, err := skillapi.NewHTTPHandler(skillService, skillapi.HTTPOptions{
		Token: config.token, Principal: config.principal,
	})
	if err != nil {
		return err
	}
	var agentHandler http.Handler
	var internalAgent *agentdaemon.Daemon
	if config.agentConfig != "" {
		internalAgent, err = agentdaemon.Open(agentdaemon.Options{
			Config: agentConfig, DataDir: config.dataDir, Control: service,
			HTTPToken: config.agentToken, APIKey: config.agentAPIKey,
			Skills: catalog, LearnedSkills: learnedSkills, Personas: personas,
			Memory: sharedMemory, OutcomesRecordedByControl: true,
			PlanStore: planStore, Signals: signals,
		})
		if err != nil {
			return fmt.Errorf("start internal Agent Runtime: %w", err)
		}
		defer func() {
			result = errors.Join(result, internalAgent.Close())
		}()
		agentHandler = internalAgent.Handler()
	}
	var taskManagers []managementapi.TaskManager
	if internalAgent != nil {
		taskManagers = append(taskManagers, internalAgent)
	}
	managementService, err := managementapi.New(personas, sharedMemory, taskManagers...)
	if err != nil {
		return err
	}
	if err := managementService.ConfigureSkills(catalog, learnedSkills); err != nil {
		return err
	}
	if err := managementService.ConfigureControl(
		service, managementPrincipal(config.principal),
	); err != nil {
		return err
	}
	managementHandler, err := managementapi.NewHTTPHandler(
		managementService, managementapi.HTTPOptions{Token: config.token},
	)
	if err != nil {
		return err
	}
	rootHandler := composeHandlers(
		handler, skillHandler, agentHandler, planHandler, signalHandler, managementHandler,
		consoleui.NewHandler(),
	)
	listener, err := net.Listen("tcp", config.address)
	if err != nil {
		return fmt.Errorf("listen for Host Control: %w", err)
	}
	server := &http.Server{
		Handler:           rootHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       45 * time.Second,
	}
	httpResult := make(chan error, 1)
	go func() {
		httpResult <- server.Serve(listener)
	}()
	fmt.Fprintf(
		stderr,
		"rin-control: listening on %s as %s\n",
		listener.Addr(),
		config.principal.ID,
	)
	if internalAgent != nil {
		fmt.Fprintln(stderr, "rin-control: internal Agent Runtime enabled")
	}

	httpDone := false
	select {
	case <-ctx.Done():
	case err := <-httpResult:
		httpDone = true
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve Host Control: %w", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		shutdownTimeout,
	)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, fmt.Errorf("stop Host Control: %w", err))
	}
	if !httpDone {
		if err := <-httpResult; !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, fmt.Errorf("serve Host Control: %w", err))
		}
	}
	return result
}

func managementPrincipal(principal host.Principal) host.Principal {
	seen := make(map[string]struct{}, len(principal.GrantedScopes)+5)
	scopes := make([]string, 0, len(principal.GrantedScopes)+5)
	for _, scope := range append(
		append([]string(nil), principal.GrantedScopes...),
		controlplane.ScopeActorRead,
		controlplane.ScopeActorControl,
		controlplane.ScopeOperationCancel,
		controlplane.ScopeHostAdmin,
		"rin.policy.confirm",
	) {
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	return host.Principal{ID: principal.ID, GrantedScopes: scopes}
}

func parseConfiguration(
	arguments []string,
	lookupEnv func(string) (string, bool),
	stderr io.Writer,
) (configuration, error) {
	flags := flag.NewFlagSet("rin-control", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String(
		"control-addr",
		envOr(lookupEnv, "RIN_CONTROL_ADDR", "127.0.0.1:7375"),
		"loopback Host Control listen address",
	)
	dataDirectory := flags.String(
		"data",
		envOr(lookupEnv, "RIN_CONTROL_DATA_DIR", "./rin-control-data"),
		"Control Operation state directory",
	)
	principalID := flags.String(
		"principal",
		envOr(lookupEnv, "RIN_CONTROL_PRINCIPAL", ""),
		"trusted game principal exposed to MCP clients",
	)
	scopesText := flags.String(
		"scopes",
		envOr(
			lookupEnv,
			"RIN_CONTROL_SCOPES",
			controlplane.ScopeActorRead+","+skillapi.ScopeSkillRead,
		),
		"comma-separated Control Plane scopes",
	)
	policyPath := flags.String(
		"policy",
		envOr(lookupEnv, "RIN_CONTROL_POLICY", ""),
		"optional gameplay policy JSON file; the built-in default denies unknown effects",
	)
	agentConfigPath := flags.String(
		"agent-config",
		envOr(lookupEnv, "RIN_AGENT_CONFIG", ""),
		"optional private internal Agent Runtime JSON configuration",
	)
	if err := flags.Parse(arguments); err != nil {
		return configuration{}, err
	}
	if flags.NArg() != 0 {
		return configuration{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if err := validateLoopbackAddress(*address); err != nil {
		return configuration{}, err
	}
	token, _ := lookupEnv("RIN_CONTROL_TOKEN")
	if len(token) < 32 {
		return configuration{}, errors.New(
			"RIN_CONTROL_TOKEN must contain at least 32 bytes",
		)
	}
	scopes, err := parseScopes(*scopesText)
	if err != nil {
		return configuration{}, err
	}
	principal := host.Principal{
		ID:            strings.TrimSpace(*principalID),
		GrantedScopes: scopes,
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return configuration{}, fmt.Errorf(
			"invalid Control Plane principal: %w",
			err,
		)
	}
	if !containsAnyControlScope(scopes) {
		return configuration{}, errors.New(
			"RIN_CONTROL_SCOPES requires at least one Control Plane scope",
		)
	}
	agentConfig := strings.TrimSpace(*agentConfigPath)
	agentToken, agentTokenSet := lookupEnv("RIN_AGENT_TOKEN")
	agentAPIKey, agentAPIKeySet := lookupEnv("RIN_AGENT_API_KEY")
	agentEmbeddingAPIKey, agentEmbeddingAPIKeySet := lookupEnv("RIN_AGENT_EMBEDDING_API_KEY")
	if agentConfig == "" {
		if agentTokenSet || agentAPIKeySet || agentEmbeddingAPIKeySet {
			return configuration{}, errors.New(
				"RIN_AGENT_TOKEN, RIN_AGENT_API_KEY, and RIN_AGENT_EMBEDDING_API_KEY require --agent-config",
			)
		}
	} else if len(agentToken) < 32 {
		return configuration{}, errors.New(
			"RIN_AGENT_TOKEN must contain at least 32 bytes when Agent Runtime is enabled",
		)
	} else if agentToken == token {
		return configuration{}, errors.New(
			"RIN_AGENT_TOKEN must differ from RIN_CONTROL_TOKEN",
		)
	} else if agentAPIKey != "" && (agentAPIKey == agentToken || agentAPIKey == token) {
		return configuration{}, errors.New(
			"RIN_AGENT_API_KEY must differ from daemon tokens",
		)
	} else if agentEmbeddingAPIKey != "" &&
		(agentEmbeddingAPIKey == agentToken || agentEmbeddingAPIKey == token) {
		return configuration{}, errors.New(
			"RIN_AGENT_EMBEDDING_API_KEY must differ from daemon tokens",
		)
	}
	return configuration{
		address: *address, dataDir: *dataDirectory, token: token,
		principal: principal, policy: strings.TrimSpace(*policyPath),
		agentConfig: agentConfig, agentToken: agentToken, agentAPIKey: agentAPIKey,
		agentEmbeddingAPIKey: agentEmbeddingAPIKey,
	}, nil
}

func composeHandlers(
	controlHandler, skillHandler, agentHandler http.Handler,
	additional ...http.Handler,
) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", controlHandler)
	mux.Handle("/control/", controlHandler)
	mux.Handle("/skills/", skillHandler)
	if len(additional) != 0 && additional[0] != nil {
		mux.Handle("/plans/", additional[0])
	}
	if len(additional) > 1 && additional[1] != nil {
		mux.Handle("/signals/", additional[1])
	}
	if len(additional) > 2 && additional[2] != nil {
		mux.Handle("/management/", additional[2])
	}
	if len(additional) > 3 && additional[3] != nil {
		mux.Handle("/console", additional[3])
		mux.Handle("/console/", additional[3])
	}
	if agentHandler != nil {
		mux.Handle("/agent/", agentHandler)
	}
	return mux
}

func loadPolicyEngine(path string) (*policy.Engine, error) {
	config := policy.Config{
		Revision:         1,
		Profile:          policy.ProfileGuarded,
		KnownEffectKinds: []string{},
		KnownScopes:      []string{},
		ConfirmationTTL: policy.ConfirmationDurations{
			Event: 16, Step: 600, Realtime: 30_000,
		},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	}
	if path != "" {
		config = policy.Config{}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open gameplay policy: %w", err)
		}
		payload, readErr := io.ReadAll(io.LimitReader(file, maxPolicyBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read gameplay policy: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close gameplay policy: %w", closeErr)
		}
		if int64(len(payload)) > maxPolicyBytes {
			return nil, errors.New("gameplay policy exceeds 1 MiB")
		}
		if err := jsonwire.Validate(payload); err != nil {
			return nil, fmt.Errorf("invalid gameplay policy JSON: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("decode gameplay policy: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, errors.New("gameplay policy must contain one JSON value")
		}
	}
	engine, err := policy.New(config)
	if err != nil {
		return nil, fmt.Errorf("validate gameplay policy: %w", err)
	}
	return engine, nil
}

func parseScopes(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	scopes := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		scope := strings.TrimSpace(part)
		if scope == "" {
			return nil, errors.New("Control Plane scopes must not be empty")
		}
		if _, exists := seen[scope]; exists {
			return nil, fmt.Errorf("duplicate Control Plane scope %q", scope)
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

func validateLoopbackAddress(address string) error {
	hostName, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid Host Control address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return errors.New("invalid Host Control port")
	}
	if strings.EqualFold(hostName, "localhost") {
		return nil
	}
	ip := net.ParseIP(hostName)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("Host Control address must use a loopback IP")
	}
	return nil
}

func containsScope(scopes []string, expected string) bool {
	for _, scope := range scopes {
		if scope == expected {
			return true
		}
	}
	return false
}

func containsAnyControlScope(scopes []string) bool {
	for _, scope := range []string{
		controlplane.ScopeActorRead,
		controlplane.ScopeActorControl,
		controlplane.ScopeActorExecute,
		controlplane.ScopeOperationCancel,
		controlplane.ScopeHostAdmin,
	} {
		if containsScope(scopes, scope) {
			return true
		}
	}
	return false
}

func envOr(
	lookupEnv func(string) (string, bool),
	name string,
	fallback string,
) string {
	if value, exists := lookupEnv(name); exists {
		return value
	}
	return fallback
}

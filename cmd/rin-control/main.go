package main

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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/agentdaemon"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/skillapi"
)

const shutdownTimeout = 5 * time.Second
const maxPolicyBytes int64 = 1 << 20

type configuration struct {
	address     string
	dataDir     string
	token       string
	principal   host.Principal
	policy      string
	agentConfig string
	agentToken  string
	agentAPIKey string
}

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		shutdownSignals()...,
	)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "rin-control:", err)
		os.Exit(1)
	}
}

func run(
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
	outcomeSink, err := cognition.NewOutcomeMemorySink(memory)
	if err != nil {
		return fmt.Errorf("create Outcome memory projection: %w", err)
	}
	service, err := controlplane.OpenFile(
		config.dataDir,
		controlplane.Options{
			PolicyEngine: policyEngine,
			OutcomeSink:  outcomeSink,
		},
	)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, service.Close())
	}()
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
			Skills: catalog, LearnedSkills: learnedSkills,
			Memory: memory, OutcomesRecordedByControl: true,
		})
		if err != nil {
			return fmt.Errorf("start internal Agent Runtime: %w", err)
		}
		defer func() {
			result = errors.Join(result, internalAgent.Close())
		}()
		agentHandler = internalAgent.Handler()
	}
	rootHandler := composeHandlers(handler, skillHandler, agentHandler)
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
	if agentConfig == "" {
		if agentTokenSet || agentAPIKeySet {
			return configuration{}, errors.New(
				"RIN_AGENT_TOKEN and RIN_AGENT_API_KEY require --agent-config",
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
	}
	return configuration{
		address: *address, dataDir: *dataDirectory, token: token,
		principal: principal, policy: strings.TrimSpace(*policyPath),
		agentConfig: agentConfig, agentToken: agentToken, agentAPIKey: agentAPIKey,
	}, nil
}

func composeHandlers(
	controlHandler, skillHandler, agentHandler http.Handler,
) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/control/", controlHandler)
	mux.Handle("/skills/", skillHandler)
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
			Event:    16,
			Step:     600,
			Realtime: 30_000,
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

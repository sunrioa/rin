package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/mcpbridge"
)

const shutdownTimeout = 5 * time.Second

type configuration struct {
	address   string
	token     string
	principal host.Principal
}

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		shutdownSignals()...,
	)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "rin-mcp:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	arguments []string,
	lookupEnv func(string) (string, bool),
	stderr io.Writer,
) error {
	config, err := parseConfiguration(arguments, lookupEnv, stderr)
	if err != nil {
		return err
	}

	service := controlplane.New(controlplane.Options{})
	handler, err := controlplane.NewHTTPHandler(
		service,
		controlplane.HTTPOptions{Token: config.token},
	)
	if err != nil {
		return err
	}
	gateway, err := mcpbridge.New(service, config.principal)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.address)
	if err != nil {
		return fmt.Errorf("listen for Host Control: %w", err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpResult := make(chan error, 1)
	go func() {
		httpResult <- server.Serve(listener)
	}()
	mcpResult := make(chan error, 1)
	go func() {
		mcpResult <- gateway.Run(runCtx, &mcp.StdioTransport{})
	}()

	fmt.Fprintf(
		stderr,
		"rin-mcp: Host Control listening on %s; MCP protocol %s over stdio\n",
		listener.Addr(),
		mcpbridge.ProtocolVersion,
	)

	var result error
	httpDone := false
	mcpDone := false
	select {
	case <-ctx.Done():
	case err := <-httpResult:
		httpDone = true
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve Host Control: %w", err)
		}
	case err := <-mcpResult:
		mcpDone = true
		if err != nil && !errors.Is(err, context.Canceled) {
			result = fmt.Errorf("serve MCP: %w", err)
		}
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		shutdownTimeout,
	)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, fmt.Errorf("stop Host Control: %w", err))
	}
	if !httpDone {
		if err := <-httpResult; !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, fmt.Errorf("serve Host Control: %w", err))
		}
	}
	if !mcpDone {
		if err := <-mcpResult; err != nil && !errors.Is(err, context.Canceled) {
			result = errors.Join(result, fmt.Errorf("serve MCP: %w", err))
		}
	}
	return result
}

func parseConfiguration(
	arguments []string,
	lookupEnv func(string) (string, bool),
	stderr io.Writer,
) (configuration, error) {
	flags := flag.NewFlagSet("rin-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String(
		"control-addr",
		envOr(lookupEnv, "RIN_CONTROL_ADDR", "127.0.0.1:7375"),
		"loopback Host Control listen address",
	)
	principalID := flags.String(
		"principal",
		envOr(lookupEnv, "RIN_CONTROL_PRINCIPAL", ""),
		"trusted game principal exposed to this MCP process",
	)
	scopesText := flags.String(
		"scopes",
		envOr(lookupEnv, "RIN_CONTROL_SCOPES", controlplane.ScopeActorRead),
		"comma-separated Control Plane scopes",
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
		return configuration{}, fmt.Errorf("invalid Control Plane principal: %w", err)
	}
	if !containsScope(scopes, controlplane.ScopeActorRead) &&
		!containsScope(scopes, controlplane.ScopeHostAdmin) {
		return configuration{}, errors.New(
			"RIN_CONTROL_SCOPES requires actor.read or host.admin",
		)
	}
	return configuration{
		address:   *address,
		token:     token,
		principal: principal,
	}, nil
}

func parseScopes(value string) ([]string, error) {
	allowed := map[string]struct{}{
		controlplane.ScopeActorRead:       {},
		controlplane.ScopeActorConverse:   {},
		controlplane.ScopeActorDirect:     {},
		controlplane.ScopeActorExecute:    {},
		controlplane.ScopeOperationCancel: {},
		controlplane.ScopeHostAdmin:       {},
	}
	parts := strings.Split(value, ",")
	scopes := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		scope := strings.TrimSpace(part)
		if _, exists := allowed[scope]; !exists {
			return nil, fmt.Errorf("unsupported Control Plane scope %q", scope)
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

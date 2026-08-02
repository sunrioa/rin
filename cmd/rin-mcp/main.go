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
	"github.com/sunrioa/rin/internal/mcpconfig"
	"github.com/sunrioa/rin/mcpbridge"
)

const shutdownTimeout = 5 * time.Second

type configuration struct {
	controlURL         string
	token              string
	conformanceAddress string
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
	client, err := controlplane.NewHTTPClient(config.controlURL, config.token)
	if err != nil {
		return err
	}
	info, err := client.Info(ctx)
	if err != nil {
		return fmt.Errorf("connect to rin-control: %w", err)
	}
	gateway, err := mcpbridge.NewClient(client, info.Principal)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		stderr,
		"rin-mcp: connected to %s as %s; MCP protocol up to %s with SDK negotiation\n",
		config.controlURL,
		info.Principal.ID,
		mcpbridge.ProtocolVersion,
	)
	if config.conformanceAddress != "" {
		if !readOnlyConformancePrincipal(info.Principal) {
			return errors.New(
				"conformance HTTP requires an actor.read-only daemon principal",
			)
		}
		return runConformanceHTTP(
			ctx,
			config.conformanceAddress,
			gateway.Server(),
			stderr,
		)
	}
	if err := gateway.Run(ctx, &mcp.StdioTransport{}); err != nil &&
		!errors.Is(err, context.Canceled) {
		return fmt.Errorf("serve MCP: %w", err)
	}
	return nil
}

func parseConfiguration(
	arguments []string,
	lookupEnv func(string) (string, bool),
	stderr io.Writer,
) (configuration, error) {
	flags := flag.NewFlagSet("rin-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String(
		"config",
		envOr(lookupEnv, "RIN_MCP_CONFIG", ""),
		"private MCP client configuration file",
	)
	controlURL := flags.String(
		"control-url",
		"",
		"loopback rin-control base URL",
	)
	conformanceAddress := flags.String(
		"conformance-addr",
		"",
		"serve read-only stateless MCP HTTP for official conformance",
	)
	if err := flags.Parse(arguments); err != nil {
		return configuration{}, err
	}
	if flags.NArg() != 0 {
		return configuration{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	token, _ := lookupEnv("RIN_CONTROL_TOKEN")
	if *configPath != "" {
		fileConfig, err := mcpconfig.Load(*configPath)
		if err != nil {
			return configuration{}, fmt.Errorf("load MCP configuration: %w", err)
		}
		token = fileConfig.Token
		if *controlURL == "" {
			*controlURL = fileConfig.ControlURL
		}
	} else if *controlURL == "" {
		*controlURL = envOr(lookupEnv, "RIN_CONTROL_URL", "")
	}
	if *controlURL == "" {
		*controlURL = "http://127.0.0.1:7375"
	}
	if len(token) < 32 {
		return configuration{}, errors.New(
			"RIN_CONTROL_TOKEN or configured token must contain at least 32 bytes",
		)
	}
	if _, err := controlplane.NewHTTPClient(*controlURL, token); err != nil {
		return configuration{}, err
	}
	if *conformanceAddress != "" {
		if err := validateLoopbackAddress(*conformanceAddress); err != nil {
			return configuration{}, err
		}
	}
	return configuration{
		controlURL:         *controlURL,
		token:              token,
		conformanceAddress: *conformanceAddress,
	}, nil
}

func runConformanceHTTP(
	ctx context.Context,
	address string,
	mcpServer *mcp.Server,
	stderr io.Writer,
) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for MCP conformance: %w", err)
	}
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
		},
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("GET /health", func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       45 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(listener)
	}()
	fmt.Fprintf(
		stderr,
		"rin-mcp: conformance HTTP listening on %s/mcp\n",
		listener.Addr(),
	)
	select {
	case <-ctx.Done():
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve MCP conformance: %w", err)
		}
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		shutdownTimeout,
	)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("stop MCP conformance: %w", err)
	}
	if err := <-result; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve MCP conformance: %w", err)
	}
	return nil
}

func readOnlyConformancePrincipal(principal host.Principal) bool {
	if len(principal.GrantedScopes) != 1 {
		return false
	}
	return principal.GrantedScopes[0] == controlplane.ScopeActorRead
}

func validateLoopbackAddress(address string) error {
	hostName, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid MCP conformance address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return errors.New("invalid MCP conformance port")
	}
	if strings.EqualFold(hostName, "localhost") {
		return nil
	}
	ip := net.ParseIP(hostName)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("MCP conformance address must use a loopback IP")
	}
	return nil
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

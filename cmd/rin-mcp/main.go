package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/mcpbridge"
)

type configuration struct {
	controlURL string
	token      string
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
		"rin-mcp: connected to %s as %s; MCP protocol up to %s over stdio with SDK negotiation\n",
		config.controlURL,
		info.Principal.ID,
		mcpbridge.ProtocolVersion,
	)
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
	controlURL := flags.String(
		"control-url",
		envOr(lookupEnv, "RIN_CONTROL_URL", "http://127.0.0.1:7375"),
		"loopback rin-control base URL",
	)
	if err := flags.Parse(arguments); err != nil {
		return configuration{}, err
	}
	if flags.NArg() != 0 {
		return configuration{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	token, _ := lookupEnv("RIN_CONTROL_TOKEN")
	if len(token) < 32 {
		return configuration{}, errors.New(
			"RIN_CONTROL_TOKEN must contain at least 32 bytes",
		)
	}
	if _, err := controlplane.NewHTTPClient(*controlURL, token); err != nil {
		return configuration{}, err
	}
	return configuration{
		controlURL: *controlURL,
		token:      token,
	}, nil
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

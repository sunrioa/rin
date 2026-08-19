package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultConsoleURL   = "http://127.0.0.1:7375"
	consoleCheckTimeout = 5 * time.Second
)

type consoleOptions struct {
	url  string
	open bool
}

func runConsole(
	arguments []string,
	output io.Writer,
	errorOutput io.Writer,
	lookupEnv func(string) (string, bool),
) error {
	return runConsoleWithOpener(
		arguments, output, errorOutput, lookupEnv, openConsoleInBrowser,
	)
}

func runConsoleWithOpener(
	arguments []string,
	output io.Writer,
	errorOutput io.Writer,
	lookupEnv func(string) (string, bool),
	open func(string) error,
) error {
	return runConsoleWithOpenerAndClient(
		arguments, output, errorOutput, lookupEnv, open,
		&http.Client{Timeout: consoleCheckTimeout},
	)
}

func runConsoleWithOpenerAndClient(
	arguments []string,
	output io.Writer,
	errorOutput io.Writer,
	lookupEnv func(string) (string, bool),
	open func(string) error,
	client *http.Client,
) error {
	options, err := parseConsoleOptions(arguments, errorOutput, lookupEnv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	baseURL, err := parseConsoleURL(options.url)
	if err != nil {
		return err
	}
	if err := checkConsoleServiceWithClient(context.Background(), baseURL, client); err != nil {
		return err
	}
	consoleURL := strings.TrimRight(baseURL.String(), "/") + "/console/"
	if _, err := fmt.Fprintf(output, "Rin Console: %s\n", consoleURL); err != nil {
		return err
	}
	if options.open {
		if err := open(consoleURL); err != nil {
			return fmt.Errorf("open Rin Console: %w", err)
		}
	}
	return nil
}

func parseConsoleOptions(
	arguments []string,
	errorOutput io.Writer,
	lookupEnv func(string) (string, bool),
) (consoleOptions, error) {
	baseURL := ""
	if value, exists := lookupEnv("RIN_CONTROL_URL"); exists {
		baseURL = value
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultConsoleURL
	}
	flags := flag.NewFlagSet("rin console", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	options := consoleOptions{url: baseURL, open: true}
	flags.StringVar(&options.url, "url", options.url, "loopback rin-control base URL")
	flags.BoolVar(&options.open, "open", options.open, "open /console/ in the system browser")
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), `Usage:
  rin console [options]

Checks the loopback Rin service before opening its HTTP console.

Options:
`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return consoleOptions{}, flag.ErrHelp
		}
		return consoleOptions{}, err
	}
	if flags.NArg() != 0 {
		return consoleOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return options, nil
}

func parseConsoleURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("console URL must be a plain loopback HTTP origin")
	}
	hostName, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil || portText == "" {
		return nil, errors.New("console URL requires an explicit port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65_535 {
		return nil, errors.New("console URL has an invalid port")
	}
	if !strings.EqualFold(hostName, "localhost") {
		ip := net.ParseIP(hostName)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("console URL must use a loopback host")
		}
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func checkConsoleService(ctx context.Context, baseURL *url.URL) error {
	return checkConsoleServiceWithClient(
		ctx, baseURL, &http.Client{Timeout: consoleCheckTimeout},
	)
}

func checkConsoleServiceWithClient(
	ctx context.Context,
	baseURL *url.URL,
	client *http.Client,
) error {
	healthURL := *baseURL
	healthURL.Path = "/control/v2/health"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL.String(), nil)
	if err != nil {
		return fmt.Errorf("build Rin service check: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("check Rin service: %w", err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4096)); err != nil {
		return fmt.Errorf("read Rin service check: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Rin service check returned %s", response.Status)
	}
	return nil
}

func openConsoleInBrowser(target string) error {
	var command string
	var arguments []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		arguments = []string{target}
	case "windows":
		command = "rundll32"
		arguments = []string{"url.dll,FileProtocolHandler", target}
	default:
		command = "xdg-open"
		arguments = []string{target}
	}
	return exec.Command(command, arguments...).Run()
}

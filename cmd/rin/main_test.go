package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestSidecarProcessPersistsAndReleasesDataDirectoryLock(t *testing.T) {
	if os.Getenv("RIN_SIDECAR_PROCESS_HELPER") == "1" {
		err := run([]string{
			"serve",
			"-addr", os.Getenv("RIN_SIDECAR_PROCESS_ADDR"),
			"-data", os.Getenv("RIN_SIDECAR_PROCESS_DATA"),
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}

	address := reserveLoopbackAddress(t)
	dataDirectory := filepath.Join(t.TempDir(), "rin-data")
	first, firstOutput := startSidecarProcessHelper(t, address, dataDirectory)
	firstRunning := true
	defer func() {
		if firstRunning {
			stopSidecarProcess(t, first, firstOutput)
		}
	}()
	waitForSidecarHealth(t, address, first, firstOutput)

	create := protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "request.windows-smoke.create",
		SessionID:       "session.windows-smoke",
		Binding: protocol.Binding{
			GameID:         "game.windows-smoke",
			ContentID:      "content.windows-smoke",
			ContentVersion: "1",
			ContentHash:    strings.Repeat("a", 64),
		},
		Seed:     1,
		Features: protocol.RecommendedFeatures(),
		Actors: []protocol.ActorSeed{{
			ID: "npc.windows-smoke", Kind: "npc", DisplayName: "Windows Smoke",
			ThinkEveryTicks: 1, Enabled: true,
		}},
	}
	postSidecarJSON(t, address, "/v2/session/create", create)

	second := exec.Command(os.Args[0], "-test.run=^TestSidecarProcessPersistsAndReleasesDataDirectoryLock$")
	second.Env = append(
		os.Environ(),
		"RIN_SIDECAR_PROCESS_HELPER=1",
		"RIN_SIDECAR_PROCESS_ADDR="+reserveLoopbackAddress(t),
		"RIN_SIDECAR_PROCESS_DATA="+dataDirectory,
		"RIN_POLICY=deterministic",
		"RIN_TOKEN=",
	)
	secondOutput, err := second.CombinedOutput()
	if err == nil || !strings.Contains(string(secondOutput), "data directory is already locked") {
		t.Fatalf("second Sidecar result err=%v output=%s, want directory lock failure", err, secondOutput)
	}

	stopSidecarProcess(t, first, firstOutput)
	firstRunning = false

	restarted, restartedOutput := startSidecarProcessHelper(t, address, dataDirectory)
	defer stopSidecarProcess(t, restarted, restartedOutput)
	waitForSidecarHealth(t, address, restarted, restartedOutput)
	postSidecarJSON(t, address, "/v2/session/get", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	})
}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startSidecarProcessHelper(t *testing.T, address, dataDirectory string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	command := exec.Command(os.Args[0], "-test.run=^TestSidecarProcessPersistsAndReleasesDataDirectoryLock$")
	command.Env = append(
		os.Environ(),
		"RIN_SIDECAR_PROCESS_HELPER=1",
		"RIN_SIDECAR_PROCESS_ADDR="+address,
		"RIN_SIDECAR_PROCESS_DATA="+dataDirectory,
		"RIN_POLICY=deterministic",
		"RIN_TOKEN=",
	)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command, output
}

func stopSidecarProcess(t *testing.T, command *exec.Cmd, output *bytes.Buffer) {
	t.Helper()
	if command.ProcessState != nil {
		return
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("stop Sidecar: %v\n%s", err, output.String())
	}
	if err := command.Wait(); err == nil {
		t.Fatalf("killed Sidecar exited successfully\n%s", output.String())
	}
}

func waitForSidecarHealth(t *testing.T, address string, command *exec.Cmd, output *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if command.ProcessState != nil {
			t.Fatalf("Sidecar exited before health check succeeded\n%s", output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Sidecar health check timed out\n%s", output.String())
}

func postSidecarJSON(t *testing.T, address, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(
		"http://"+address+path,
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("POST %s returned %s", path, response.Status)
	}
}

func TestVersionProjection(t *testing.T) {
	if version != protocol.ContractReleaseVersion {
		t.Fatalf("CLI version = %q, contract version = %q", version, protocol.ContractReleaseVersion)
	}
}

func TestRootHelpReturnsSuccess(t *testing.T) {
	if err := run([]string{"--help"}); err != nil {
		t.Fatalf("run(--help) returned %v", err)
	}
}

func TestValidateListenAddress(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		security  listenSecurity
		wantError bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1:7374"},
		{name: "IPv6 loopback", address: "[::1]:7374"},
		{name: "localhost", address: "localhost:7374"},
		{name: "remote denied", address: "0.0.0.0:7374", wantError: true},
		{
			name: "remote needs token", address: "0.0.0.0:7374",
			security:  listenSecurity{allowRemote: true, tlsProxy: true},
			wantError: true,
		},
		{
			name: "remote needs TLS proxy declaration", address: "0.0.0.0:7374",
			security:  listenSecurity{allowRemote: true, token: "token"},
			wantError: true,
		},
		{
			name: "remote explicit", address: "0.0.0.0:7374",
			security: listenSecurity{
				allowRemote: true, tlsProxy: true, token: "token",
			},
		},
		{name: "invalid", address: "7374", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateListenAddress(test.address, test.security)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestValidateServeEnvironmentRejectsInvalidConfiguredValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "body integer",
			key:   "RIN_MAX_BODY_BYTES",
			value: "many",
		},
		{
			name:  "positive integer",
			key:   "RIN_JOB_WORKERS",
			value: "0",
		},
		{
			name:  "positive unsigned integer",
			key:   "RIN_TRANSFER_MAX_BYTES",
			value: "-1",
		},
		{
			name:  "duration",
			key:   "RIN_REQUEST_TIMEOUT",
			value: "soon",
		},
		{
			name:  "boolean",
			key:   "RIN_MODEL_ALLOW_INSECURE",
			value: "sometimes",
		},
		{
			name:  "TLS proxy boolean",
			key:   "RIN_TLS_PROXY",
			value: "sometimes",
		},
		{
			name:  "bearer whitespace",
			key:   "RIN_TOKEN",
			value: "not a bearer token",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearServeEnvironment(t)
			t.Setenv(test.key, test.value)
			err := validateServeEnvironment()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("error = %v, want named environment failure", err)
			}
		})
	}

	clearServeEnvironment(t)
	t.Setenv("RIN_SESSION_SOFT_LIMIT_BYTES", "0")
	t.Setenv("RIN_REQUEST_TIMEOUT", "250ms")
	t.Setenv("RIN_MODEL_ALLOW_INSECURE", "false")
	t.Setenv("RIN_TLS_PROXY", "true")
	if err := validateServeEnvironment(); err != nil {
		t.Fatalf("valid environment rejected: %v", err)
	}
}

func TestValidateServeConfigurationRejectsExplicitFallbackValues(
	t *testing.T,
) {
	valid := serveConfiguration{
		maxBodyBytes:              1,
		maxSessionStateBytes:      1,
		maxTransferBytes:          1,
		maxTransferEvents:         1,
		maxConcurrentTransfers:    1,
		requestTimeout:            time.Millisecond,
		transferTimeout:           time.Millisecond,
		transferInactivityTimeout: time.Millisecond,
		scrubInterval:             time.Millisecond,
		scrubTimeout:              time.Millisecond,
		scrubMaxEvents:            1,
	}
	tests := []struct {
		name   string
		mutate func(*serveConfiguration)
	}{
		{
			name: "body",
			mutate: func(config *serveConfiguration) {
				config.maxBodyBytes = 0
			},
		},
		{
			name: "Session State",
			mutate: func(config *serveConfiguration) {
				config.maxSessionStateBytes = 0
			},
		},
		{
			name: "Transfer concurrency",
			mutate: func(config *serveConfiguration) {
				config.maxConcurrentTransfers = -1
			},
		},
		{
			name: "ordinary timeout",
			mutate: func(config *serveConfiguration) {
				config.requestTimeout = 0
			},
		},
		{
			name: "Session limits",
			mutate: func(config *serveConfiguration) {
				config.sessionSoftLimitBytes = 2
				config.sessionHardLimitBytes = 1
			},
		},
		{
			name: "scrub budget",
			mutate: func(config *serveConfiguration) {
				config.scrubMaxEvents = rinruntime.MaxScrubEventBudget + 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if err := validateServeConfiguration(config); err == nil {
				t.Fatal("invalid explicit configuration was accepted")
			}
		})
	}
	if err := validateServeConfiguration(valid); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
}

func TestRunScrubLoopStartsImmediatelyAndStopsWithContext(t *testing.T) {
	scrubber := &blockingScrubber{calls: make(chan int, 1)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runScrubLoop(
			ctx,
			scrubber,
			logger,
			time.Hour,
			time.Hour,
			17,
		)
	}()
	select {
	case budget := <-scrubber.calls:
		if budget != 17 {
			t.Fatalf("scrub budget = %d", budget)
		}
	case <-time.After(time.Second):
		t.Fatal("background scrub did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background scrub did not stop")
	}
}

type blockingScrubber struct {
	calls chan int
}

func (s *blockingScrubber) Scrub(
	ctx context.Context,
	maxEvents int,
) (rinruntime.ScrubReport, error) {
	s.calls <- maxEvents
	<-ctx.Done()
	return rinruntime.ScrubReport{}, ctx.Err()
}

func TestServeFailsBeforeStartupForInvalidConfiguredLimits(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		clearServeEnvironment(t)
		t.Setenv("RIN_REQUEST_TIMEOUT", "never")
		err := run([]string{"serve"})
		if err == nil ||
			!strings.Contains(err.Error(), "RIN_REQUEST_TIMEOUT") {
			t.Fatalf("run error = %v", err)
		}
	})
	t.Run("flag", func(t *testing.T) {
		clearServeEnvironment(t)
		err := run([]string{"serve", "-max-body-bytes", "0"})
		if err == nil ||
			!strings.Contains(err.Error(), "max-body-bytes") {
			t.Fatalf("run error = %v", err)
		}
	})
	t.Run("remote listener without TLS proxy declaration", func(t *testing.T) {
		clearServeEnvironment(t)
		t.Setenv("RIN_TOKEN", "test-token")
		dataDirectory := filepath.Join(t.TempDir(), "must-not-exist")
		err := run([]string{
			"serve",
			"-addr", "0.0.0.0:7374",
			"-allow-remote",
			"-data", dataDirectory,
		})
		if err == nil || !strings.Contains(err.Error(), "tls-proxy") {
			t.Fatalf("run error = %v", err)
		}
		if _, statErr := os.Stat(dataDirectory); !errors.Is(
			statErr,
			os.ErrNotExist,
		) {
			t.Fatalf("failed remote preflight changed data directory: %v", statErr)
		}
	})
	for _, test := range []struct {
		name        string
		environment bool
	}{
		{name: "remote TLS proxy flag"},
		{name: "remote TLS proxy environment", environment: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearServeEnvironment(t)
			t.Setenv("RIN_TOKEN", "test-token")
			if test.environment {
				t.Setenv("RIN_TLS_PROXY", "true")
			}
			notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			arguments := []string{
				"serve",
				"-addr", "0.0.0.0:7374",
				"-allow-remote",
				"-data", notDirectory,
			}
			if !test.environment {
				arguments = append(arguments, "-tls-proxy")
			}
			err := run(arguments)
			if err == nil || strings.Contains(err.Error(), "tls-proxy") {
				t.Fatalf("TLS proxy declaration was not accepted: %v", err)
			}
		})
	}
}

func clearServeEnvironment(t *testing.T) {
	t.Helper()
	keys := []string{"RIN_MAX_BODY_BYTES", "RIN_TOKEN"}
	keys = append(keys, positiveIntEnvironment...)
	keys = append(keys, positiveUintEnvironment...)
	keys = append(keys, nonNegativeUintEnvironment...)
	keys = append(keys, positiveDurationEnvironment...)
	keys = append(keys, booleanEnvironment...)
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

func TestValidateModelEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		insecure  bool
		wantError bool
	}{
		{name: "https", url: "https://models.example/v1"},
		{name: "local IPv4", url: "http://127.0.0.1:8080/v1"},
		{name: "local IPv6", url: "http://[::1]:8080/v1"},
		{name: "remote HTTP denied", url: "http://models.example/v1", wantError: true},
		{name: "remote HTTP explicit", url: "http://models.example/v1", insecure: true},
		{name: "userinfo denied", url: "https://user@models.example/v1", wantError: true},
		{name: "file denied", url: "file:///tmp/model", wantError: true},
		{name: "query denied", url: "https://models.example/v1?redirect=1", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateModelEndpoint(test.url, test.insecure)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestBuildPolicyModes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("RIN_POLICY", "deterministic")
	selected, mode, err := buildDecisionProvider(logger)
	if err != nil || selected == nil || mode != "deterministic" {
		t.Fatalf("deterministic policy: mode=%s err=%v", mode, err)
	}

	t.Setenv("RIN_POLICY", "model")
	t.Setenv("RIN_MODEL_BASE_URL", "")
	t.Setenv("RIN_MODEL", "")
	if _, _, err := buildDecisionProvider(logger); err == nil {
		t.Fatal("missing model configuration should fail")
	}
	t.Setenv("RIN_MODEL_BASE_URL", "http://127.0.0.1:9999/v1")
	t.Setenv("RIN_MODEL", "fixture-model")
	t.Setenv("RIN_MODEL_API_KEY", "")
	selected, mode, err = buildDecisionProvider(logger)
	if err != nil || selected == nil || mode != "model-with-fallback" {
		t.Fatalf("local model policy: mode=%s err=%v", mode, err)
	}

	t.Setenv("RIN_MODEL_BASE_URL", "https://models.example/v1")
	if _, _, err := buildDecisionProvider(logger); err == nil {
		t.Fatal("remote model without API key should fail")
	}
}

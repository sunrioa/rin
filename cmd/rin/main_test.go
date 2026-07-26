package main

import (
	"bytes"
	"encoding/json"
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
	postSidecarJSON(t, address, "/v1/session/create", create)

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
	postSidecarJSON(t, address, "/v1/session/get", protocol.SessionRequest{
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
		name        string
		address     string
		allowRemote bool
		token       string
		wantError   bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1:7374"},
		{name: "IPv6 loopback", address: "[::1]:7374"},
		{name: "localhost", address: "localhost:7374"},
		{name: "remote denied", address: "0.0.0.0:7374", wantError: true},
		{name: "remote needs token", address: "0.0.0.0:7374", allowRemote: true, wantError: true},
		{name: "remote explicit", address: "0.0.0.0:7374", allowRemote: true, token: "token"},
		{name: "invalid", address: "7374", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateListenAddress(test.address, test.allowRemote, test.token)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
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
	selected, mode, err := buildPolicy(logger)
	if err != nil || selected == nil || mode != "deterministic" {
		t.Fatalf("deterministic policy: mode=%s err=%v", mode, err)
	}

	t.Setenv("RIN_POLICY", "model")
	t.Setenv("RIN_MODEL_BASE_URL", "")
	t.Setenv("RIN_MODEL", "")
	if _, _, err := buildPolicy(logger); err == nil {
		t.Fatal("missing model configuration should fail")
	}
	t.Setenv("RIN_MODEL_BASE_URL", "http://127.0.0.1:9999/v1")
	t.Setenv("RIN_MODEL", "fixture-model")
	t.Setenv("RIN_MODEL_API_KEY", "")
	selected, mode, err = buildPolicy(logger)
	if err != nil || selected == nil || mode != "model-with-fallback" {
		t.Fatalf("local model policy: mode=%s err=%v", mode, err)
	}

	t.Setenv("RIN_MODEL_BASE_URL", "https://models.example/v1")
	if _, _, err := buildPolicy(logger); err == nil {
		t.Fatal("remote model without API key should fail")
	}
}

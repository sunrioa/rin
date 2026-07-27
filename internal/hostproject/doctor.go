package hostproject

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	runtimeProbeTimeout    = 2 * time.Second
	runtimeProbeWaitDelay  = 250 * time.Millisecond
	maxRuntimeVersionBytes = 4096
)

type RuntimeStatus string

const (
	RuntimeAvailable     RuntimeStatus = "available"
	RuntimeMissing       RuntimeStatus = "missing"
	RuntimeUnusable      RuntimeStatus = "unusable"
	RuntimeProbeTimedOut RuntimeStatus = "timed_out"
	RuntimeUnsupported   RuntimeStatus = "unsupported"
)

type DoctorReport struct {
	Conformance Report
	Platform    string
	Runtime     string
	Executable  string
	Status      RuntimeStatus
	Version     string
	Detail      string
}

type runtimeSpec struct {
	candidates []runtimeCommand
}

type runtimeCommand struct {
	executable string
	arguments  []string
}

type runtimeProbeResult struct {
	status  RuntimeStatus
	version string
	detail  string
}

var runtimeSpecs = map[string]runtimeSpec{
	"go": {candidates: []runtimeCommand{
		{executable: "go", arguments: []string{"version"}},
	}},
	"javascript": {candidates: []runtimeCommand{
		{executable: "node", arguments: []string{"--version"}},
	}},
	"python": {candidates: []runtimeCommand{
		{executable: "python3", arguments: []string{"--version"}},
		{executable: "python", arguments: []string{"--version"}},
		{executable: "py", arguments: []string{"-3", "--version"}},
	}},
	"csharp": {candidates: []runtimeCommand{
		{executable: "dotnet", arguments: []string{"--version"}},
	}},
	"java": {candidates: []runtimeCommand{
		{executable: "java", arguments: []string{"--version"}},
	}},
	"lua": {candidates: []runtimeCommand{
		{executable: "lua", arguments: []string{"-v"}},
	}},
}

func Doctor(root string) (DoctorReport, error) {
	return doctor(
		root,
		exec.LookPath,
		func(path, runtimeID string, arguments []string) runtimeProbeResult {
			return probeRuntime(path, runtimeID, arguments, runtimeProbeTimeout)
		},
	)
}

func doctor(
	root string,
	lookPath func(string) (string, error),
	probe func(string, string, []string) runtimeProbeResult,
) (DoctorReport, error) {
	report, err := Inspect(root)
	if err != nil {
		return DoctorReport{}, err
	}
	runtimeID := report.Manifest.Project.Runtime
	if runtimeID == "" {
		switch report.Manifest.Host.ID {
		case "fabric":
			runtimeID = "java"
		case "bepinex-mono", "bepinex-il2cpp":
			runtimeID = "csharp"
		case "luanti":
			runtimeID = "lua"
		}
	}
	result := DoctorReport{
		Conformance: report,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		Runtime:     runtimeID,
		Status:      RuntimeUnsupported,
		Detail:      "runtime is not supported by doctor",
	}
	spec, supported := runtimeSpecs[runtimeID]
	if !supported {
		return result, nil
	}
	result.Executable = spec.candidates[0].executable
	var firstFailure *DoctorReport
	for _, candidate := range spec.candidates {
		path, pathErr := lookPath(candidate.executable)
		if pathErr != nil {
			continue
		}
		probeResult := probe(path, runtimeID, candidate.arguments)
		candidateReport := result
		candidateReport.Executable = candidate.executable
		candidateReport.Status = probeResult.status
		candidateReport.Version = probeResult.version
		candidateReport.Detail = probeResult.detail
		if probeResult.status == RuntimeAvailable {
			return candidateReport, nil
		}
		if firstFailure == nil {
			firstFailure = &candidateReport
		}
	}
	if firstFailure != nil {
		return *firstFailure, nil
	}
	result.Status = RuntimeMissing
	result.Detail = "runtime executable was not found on PATH"
	return result, nil
}

func probeRuntime(
	path string,
	runtimeID string,
	arguments []string,
	timeout time.Duration,
) runtimeProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var output cappedBuffer
	output.remaining = maxRuntimeVersionBytes
	command := exec.CommandContext(ctx, path, arguments...)
	command.Stdout = &output
	command.Stderr = &output
	command.WaitDelay = runtimeProbeWaitDelay
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return runtimeProbeResult{
			status: RuntimeProbeTimedOut,
			detail: "runtime version probe exceeded its deadline",
		}
	}
	if err != nil {
		return runtimeProbeResult{
			status: RuntimeUnusable,
			detail: "runtime version probe could not execute successfully",
		}
	}
	version := strings.Join(strings.Fields(output.String()), " ")
	if !recognizesRuntimeVersion(runtimeID, version) {
		return runtimeProbeResult{
			status: RuntimeUnusable,
			detail: "runtime version output was not recognized",
		}
	}
	return runtimeProbeResult{status: RuntimeAvailable, version: version}
}

type cappedBuffer struct {
	bytes.Buffer
	remaining int
}

func (buffer *cappedBuffer) Write(payload []byte) (int, error) {
	size := len(payload)
	if size > buffer.remaining {
		payload = payload[:buffer.remaining]
	}
	buffer.remaining -= len(payload)
	_, _ = buffer.Buffer.Write(payload)
	return size, nil
}

func recognizesRuntimeVersion(runtimeID, version string) bool {
	lower := strings.ToLower(version)
	switch runtimeID {
	case "go":
		return strings.HasPrefix(lower, "go version go")
	case "javascript":
		return strings.HasPrefix(lower, "v") && containsDigit(version)
	case "python":
		return strings.HasPrefix(lower, "python ") && containsDigit(version)
	case "csharp":
		return containsDigit(version) && strings.Contains(version, ".")
	case "java":
		return (strings.HasPrefix(lower, "java ") ||
			strings.HasPrefix(lower, "openjdk ")) && containsDigit(version)
	case "lua":
		return strings.HasPrefix(lower, "lua ") && containsDigit(version)
	default:
		return false
	}
}

func containsDigit(value string) bool {
	for _, character := range value {
		if character >= '0' && character <= '9' {
			return true
		}
	}
	return false
}

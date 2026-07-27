package hostproject

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/hostscaffold"
	"github.com/sunrioa/rin/protocol"
)

func TestInspectCustomHostAndAddSkill(t *testing.T) {
	root := generateCustomHost(t)
	report, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Manifest.Generator.ProtocolVersion != protocol.Version ||
		report.Manifest.Project.ID != "test_host" ||
		len(report.Capabilities) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}

	target, err := AddSkill(SkillOptions{
		Root: root, ID: "movement.follow", Version: "1.2.0",
		Effect: host.EffectWorldMutation, Execution: host.ExecutionLongRunning,
		Risk: host.RiskModerate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(target) != "movement.follow@1.2.0.json" {
		t.Fatalf("target = %q", target)
	}
	report, err = Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Capabilities) != 2 {
		t.Fatalf("capability count = %d, want 2", len(report.Capabilities))
	}
	if _, err := AddSkill(SkillOptions{
		Root: root, ID: "movement.follow", Version: "1.2.0",
	}); err == nil {
		t.Fatal("duplicate capability was accepted")
	}
}

func TestInspectReportsGeneratedFileMutation(t *testing.T) {
	root := generateCustomHost(t)
	if err := os.WriteFile(
		filepath.Join(root, "src", "README.md"), []byte("changed\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ModifiedFiles) != 1 ||
		report.ModifiedFiles[0] != "src/README.md" {
		t.Fatalf("modified files = %v", report.ModifiedFiles)
	}
}

func TestDoctorReportsRuntimeWithoutRequiringIt(t *testing.T) {
	root := generateCustomHost(t)
	report, err := doctor(
		root,
		func(executable string) (string, error) {
			if executable != "python3" {
				t.Fatalf("unexpected executable lookup %q", executable)
			}
			return filepath.Join(root, "python3"), nil
		},
		func(
			path string,
			runtimeID string,
			arguments []string,
		) runtimeProbeResult {
			if path != filepath.Join(root, "python3") ||
				runtimeID != "python" ||
				len(arguments) != 1 ||
				arguments[0] != "--version" {
				t.Fatalf(
					"unexpected runtime probe: %q %q %v",
					path,
					runtimeID,
					arguments,
				)
			}
			return runtimeProbeResult{
				status:  RuntimeAvailable,
				version: "Python 3.12.1",
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Runtime != "python" || report.Executable != "python3" ||
		report.Platform == "" || report.Status != RuntimeAvailable ||
		report.Version != "Python 3.12.1" {
		t.Fatalf("unexpected doctor report: %+v", report)
	}
}

func TestDoctorDoesNotTreatFailedShimAsRuntime(t *testing.T) {
	root := generateCustomHost(t)
	report, err := doctor(
		root,
		func(string) (string, error) {
			return filepath.Join(root, "python3"), nil
		},
		func(string, string, []string) runtimeProbeResult {
			return runtimeProbeResult{
				status: RuntimeUnusable,
				detail: "runtime version probe could not execute successfully",
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != RuntimeUnusable || report.Version != "" {
		t.Fatalf("failed shim reported as a runtime: %+v", report)
	}
}

func TestDoctorUsesWorkingPythonCommandAfterStaleShim(t *testing.T) {
	root := generateCustomHost(t)
	report, err := doctor(
		root,
		func(executable string) (string, error) {
			switch executable {
			case "python3":
				return filepath.Join(root, "stale-python3"), nil
			case "python":
				return filepath.Join(root, "python"), nil
			default:
				return "", os.ErrNotExist
			}
		},
		func(path, _ string, _ []string) runtimeProbeResult {
			if filepath.Base(path) == "python" {
				return runtimeProbeResult{
					status:  RuntimeAvailable,
					version: "Python 3.12.1",
				}
			}
			return runtimeProbeResult{status: RuntimeUnusable}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != RuntimeAvailable || report.Executable != "python" {
		t.Fatalf("working Windows-compatible command was not selected: %+v", report)
	}
}

func TestRuntimeVersionProbeIsBoundedAndRecognizesRuntime(t *testing.T) {
	t.Setenv("RIN_RUNTIME_PROBE_HELPER", "success")
	success := probeRuntime(
		os.Args[0],
		"python",
		[]string{"-test.run=^TestRuntimeProbeHelper$"},
		time.Second,
	)
	if success.status != RuntimeAvailable ||
		success.version != "Python 3.12.1" {
		t.Fatalf("successful probe = %+v", success)
	}

	t.Setenv("RIN_RUNTIME_PROBE_HELPER", "hang")
	started := time.Now()
	timedOut := probeRuntime(
		os.Args[0],
		"python",
		[]string{"-test.run=^TestRuntimeProbeHelper$"},
		40*time.Millisecond,
	)
	if timedOut.status != RuntimeProbeTimedOut {
		t.Fatalf("hanging probe = %+v", timedOut)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hanging probe exceeded its bounded cleanup: %s", elapsed)
	}
}

func TestRuntimeProbeHelper(t *testing.T) {
	switch os.Getenv("RIN_RUNTIME_PROBE_HELPER") {
	case "":
		return
	case "success":
		fmt.Fprintln(os.Stdout, "Python 3.12.1")
		os.Exit(0)
	case "hang":
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(2)
	}
}

func generateCustomHost(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	result, err := hostscaffold.GenerateAt(parent, hostscaffold.Options{
		Host: hostscaffold.HostCustom, Runtime: "python",
		ID: "test_host", Output: "test-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Root
}

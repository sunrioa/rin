package hostproject

import (
	"os"
	"path/filepath"
	"testing"

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
	report, err := Doctor(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Runtime != "python" || report.Executable != "python3" ||
		report.Platform == "" {
		t.Fatalf("unexpected doctor report: %+v", report)
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

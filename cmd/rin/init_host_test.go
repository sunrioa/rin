package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunrioa/rin/release"
)

func TestInitHostListsOnlyTheGenericSkeleton(t *testing.T) {
	var first, second bytes.Buffer
	if err := runInitHost([]string{"-list-hosts"}, &first); err != nil {
		t.Fatal(err)
	}
	if err := runInitHost([]string{"-list-hosts"}, &second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("host list is not stable")
	}
	fields := strings.Fields(first.String())
	if len(fields) == 0 || fields[0] != "custom" ||
		strings.Contains(first.String(), "fabric") ||
		strings.Contains(first.String(), "luanti") ||
		strings.Contains(first.String(), "bepinex") {
		t.Fatalf("unexpected host list:\n%s", first.String())
	}
}

func TestInitHostHelpIsActionable(t *testing.T) {
	for _, arguments := range [][]string{{"--help"}, {"host", "--help"}} {
		var output bytes.Buffer
		if err := runInit(arguments, &output); err != nil {
			t.Fatalf("runInit(%v): %v", arguments, err)
		}
		for _, fragment := range []string{"Usage:", "rin init host", "self-contained"} {
			if !strings.Contains(output.String(), fragment) {
				t.Errorf("help is missing %q:\n%s", fragment, output.String())
			}
		}
		if len(arguments) > 1 && !strings.Contains(output.String(), "-runtime") {
			t.Errorf("host help is missing -runtime:\n%s", output.String())
		}
	}
}

func TestInitHostRejectsInvalidInvocation(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      string
	}{
		{"missing engine", nil, "-engine is required"},
		{"missing id", []string{"-engine", "custom", "-runtime", "go"}, "-id is required"},
		{"missing runtime", []string{"-engine", "custom", "-id", "test_host"}, "-runtime must be"},
		{"unknown host", []string{"-engine", "fabric", "-runtime", "java", "-id", "test_host"}, "unsupported host"},
		{"extra argument", []string{"-engine", "custom", "-runtime", "go", "-id", "test_host", "extra"}, "unexpected arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runInitHost(test.arguments, &output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if output.Len() != 0 {
				t.Fatalf("failed invocation wrote output: %q", output.String())
			}
		})
	}
}

func TestInitHostDryRunListsV2ContractWithoutWriting(t *testing.T) {
	workingDirectory := enterInitTestDirectory(t)
	var output bytes.Buffer
	err := runInitHost([]string{
		"-engine", "custom",
		"-runtime", "python",
		"-id", "story_host",
		"-name", "Story Host",
		"-output", "generated-host",
		"-dry-run",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(workingDirectory, "generated-host")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run created output: %v", err)
	}
	for _, fragment := range []string{
		`Would create custom scaffold "Story Host" in generated-host`,
		"rin-host.json",
		"capabilities/dialogue.say.json",
		"rin-scaffold.json",
	} {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("dry-run output is missing %q:\n%s", fragment, output.String())
		}
	}
}

func TestInitHostGeneratesGenericV2Contract(t *testing.T) {
	workingDirectory := enterInitTestDirectory(t)
	var output bytes.Buffer
	err := runInit([]string{
		"host",
		"-engine", "custom",
		"-runtime", "python",
		"-id", "story_host",
		"-name", "Story Host",
		"-author", "Example Author",
		"-output", "generated-host",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workingDirectory, "generated-host")
	for _, relative := range []string{
		".editorconfig",
		".gitignore",
		"LICENSE-RIN.txt",
		"README.md",
		"README.zh-CN.md",
		"rin-host.json",
		"capabilities/dialogue.say.json",
		"src/README.md",
		"rin-scaffold.json",
	} {
		info, statErr := os.Stat(filepath.Join(target, relative))
		if statErr != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: %v", relative, statErr)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, ".rin-scaffold.incomplete")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("completed scaffold retained incomplete marker: %v", err)
	}

	payload, err := os.ReadFile(filepath.Join(target, "rin-scaffold.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SchemaVersion int `json:"schema_version"`
		Generator     struct {
			RinVersion      string `json:"rin_version"`
			ContractVersion string `json:"contract_version"`
		} `json:"generator"`
		Project struct {
			ID      string `json:"id"`
			Runtime string `json:"runtime"`
		} `json:"project"`
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
		CapabilityProfile string `json:"capability_profile"`
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 ||
		manifest.Generator.RinVersion != release.Version ||
		manifest.Generator.ContractVersion != "rin.host/v2" ||
		manifest.Project.ID != "story_host" ||
		manifest.Project.Runtime != "python" ||
		manifest.Host.ID != "custom" ||
		manifest.CapabilityProfile != "contract-only" {
		t.Fatalf("unexpected scaffold manifest: %+v", manifest)
	}
	if !strings.Contains(output.String(), `Created custom scaffold "Story Host" in generated-host`) {
		t.Fatalf("unexpected success output:\n%s", output.String())
	}
}

func TestInitHostNeverOverwritesExistingTarget(t *testing.T) {
	workingDirectory := enterInitTestDirectory(t)
	target := filepath.Join(workingDirectory, "existing-host")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runInitHost([]string{
		"-engine", "custom",
		"-runtime", "go",
		"-id", "story_host",
		"-output", "existing-host",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want existing-target rejection", err)
	}
	payload, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(payload) != "keep\n" || output.Len() != 0 {
		t.Fatalf("existing output changed: %q, %v, output=%q", payload, readErr, output.String())
	}
}

func enterInitTestDirectory(t *testing.T) string {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := t.TempDir()
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	return workingDirectory
}

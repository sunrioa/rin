package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdkassets "github.com/sunrioa/rin/sdk"
)

func TestInitModListHostsIsStable(t *testing.T) {
	var first bytes.Buffer
	if err := runInitMod([]string{"-list-hosts"}, &first); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := runInitMod([]string{"-list-hosts"}, &second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("host list is not stable:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}

	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(first.String()), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			t.Fatalf("empty host-list line in %q", first.String())
		}
		ids = append(ids, fields[0])
	}
	want := []string{"bepinex-il2cpp", "bepinex-mono", "fabric", "luanti"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("host ids = %v, want %v\n%s", ids, want, first.String())
	}
	if !strings.Contains(first.String(), "luanti            namespace=unused") {
		t.Fatalf("Luanti namespace policy is misleading:\n%s", first.String())
	}
}

func TestInitModHelpIsActionable(t *testing.T) {
	for _, arguments := range [][]string{
		{"--help"},
		{"mod", "--help"},
		{"mod", "--host", "luanti", "--help"},
	} {
		var output bytes.Buffer
		if err := runInit(arguments, &output); err != nil {
			t.Fatalf("runInit(%v): %v", arguments, err)
		}
		for _, fragment := range []string{"Usage:", "rin init mod", "self-contained"} {
			if !strings.Contains(output.String(), fragment) {
				t.Errorf("runInit(%v) help is missing %q:\n%s", arguments, fragment, output.String())
			}
		}
	}
}

func TestInitModHelpTokenCanBeAFlagValue(t *testing.T) {
	enterInitTestDirectory(t)
	var output bytes.Buffer
	err := runInitMod([]string{
		"--host", "luanti",
		"--id", "test_mod",
		"--name", "--help",
		"--dry-run",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `scaffold "--help"`) {
		t.Fatalf("display name was mistaken for a help flag:\n%s", output.String())
	}
}

func TestInitModRejectsInvalidInvocation(t *testing.T) {
	tests := []struct {
		name      string
		run       func(io *bytes.Buffer) error
		wantError string
	}{
		{
			name: "missing resource",
			run: func(output *bytes.Buffer) error {
				return runInit(nil, output)
			},
			wantError: "init requires a resource type",
		},
		{
			name: "unknown resource",
			run: func(output *bytes.Buffer) error {
				return runInit([]string{"plugin"}, output)
			},
			wantError: `unsupported init resource "plugin"`,
		},
		{
			name: "missing host",
			run: func(output *bytes.Buffer) error {
				return runInitMod(nil, output)
			},
			wantError: "-host is required",
		},
		{
			name: "missing id",
			run: func(output *bytes.Buffer) error {
				return runInitMod([]string{"-host", "luanti"}, output)
			},
			wantError: "-id is required",
		},
		{
			name: "unknown host",
			run: func(output *bytes.Buffer) error {
				return runInitMod([]string{"-host", "unknown", "-id", "test_mod"}, output)
			},
			wantError: `unsupported host "unknown"`,
		},
		{
			name: "extra argument",
			run: func(output *bytes.Buffer) error {
				return runInitMod([]string{"-host", "luanti", "-id", "test_mod", "extra"}, output)
			},
			wantError: "unexpected arguments: [extra]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := test.run(&output)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want fragment %q", err, test.wantError)
			}
			if output.Len() != 0 {
				t.Fatalf("failed invocation wrote output: %q", output.String())
			}
		})
	}
}

func TestInitModDryRunListsFilesWithoutWriting(t *testing.T) {
	workingDirectory := enterInitTestDirectory(t)
	target := filepath.Join(workingDirectory, "generated_mod")

	var output bytes.Buffer
	err := runInitMod([]string{
		"-host", "luanti",
		"-id", "test_mod",
		"-name", "Test Mod",
		"-output", "generated_mod",
		"-dry-run",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run created target or returned unexpected stat error: %v", err)
	}
	for _, fragment := range []string{
		`Would create luanti scaffold "Test Mod" in generated_mod`,
		"README.md",
		"init.lua",
		"rin-scaffold.json",
	} {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("dry-run output is missing %q:\n%s", fragment, output.String())
		}
	}
}

func TestInitModGeneratesProject(t *testing.T) {
	workingDirectory := enterInitTestDirectory(t)
	target := filepath.Join(workingDirectory, "generated_mod")

	var output bytes.Buffer
	err := runInit([]string{
		"mod",
		"-host", "luanti",
		"-id", "test_mod",
		"-name", "Test Mod",
		"-author", "Test_Author",
		"-output", "generated_mod",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		".editorconfig",
		".gitignore",
		"LICENSE-RIN.txt",
		"README.md",
		"README.zh-CN.md",
		"init.lua",
		"mod.conf",
		"rin.lua",
		"rin-scaffold.json",
		"state.lua",
		"test_state.lua",
	} {
		info, err := os.Stat(filepath.Join(target, relative))
		if err != nil {
			t.Errorf("%s: %v", relative, err)
			continue
		}
		if !info.Mode().IsRegular() {
			t.Errorf("%s is not a regular file", relative)
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
		Generator struct {
			RinVersion string `json:"rin_version"`
		} `json:"generator"`
		Project struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"project"`
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
		CapabilityProfile string `json:"capability_profile"`
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Generator.RinVersion != sdkassets.Version ||
		manifest.Project.ID != "test_mod" ||
		manifest.Project.Name != "Test Mod" ||
		manifest.Project.Version != "0.1.0" ||
		manifest.Host.ID != "luanti" ||
		manifest.CapabilityProfile != "advisory" {
		t.Fatalf("unexpected scaffold manifest: %+v", manifest)
	}
	for _, fragment := range []string{
		`Created luanti scaffold "Test Mod" in generated_mod`,
		"generated_mod/README.md",
	} {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("success output is missing %q:\n%s", fragment, output.String())
		}
	}
}

func TestInitModDoesNotOverwriteExistingTarget(t *testing.T) {
	workingDirectory := enterInitTestDirectory(t)
	target := filepath.Join(workingDirectory, "existing_mod")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err := runInitMod([]string{
		"-host", "luanti",
		"-id", "test_mod",
		"-output", "existing_mod",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want existing-target rejection", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed generation wrote success output: %q", output.String())
	}
	payload, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "keep\n" {
		t.Fatalf("existing file changed to %q", payload)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("existing target was modified: %v", entries)
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

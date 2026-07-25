package compat_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type integrationDebtManifest struct {
	Version int `json:"version"`
	Files   []struct {
		Path     string `json:"path"`
		MaxLines int    `json:"max_lines"`
		Profile  string `json:"profile"`
	} `json:"files"`
}

func TestHostIntegrationDebtDoesNotGrow(t *testing.T) {
	payload, err := os.ReadFile("integration_debt.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest integrationDebtManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || len(manifest.Files) == 0 {
		t.Fatal("integration debt manifest is empty or has an unsupported version")
	}

	seen := make(map[string]bool)
	for _, item := range manifest.Files {
		if item.MaxLines <= 0 || seen[item.Path] {
			t.Fatalf("invalid integration debt entry for %q", item.Path)
		}
		seen[item.Path] = true
		if item.Profile != "advisory" &&
			item.Profile != "idempotent-action" &&
			item.Profile != "transactional-action" {
			t.Fatalf("%s has unknown host profile %q", item.Path, item.Profile)
		}
		file, err := os.Open(filepath.Join("..", filepath.FromSlash(item.Path)))
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		lines := 0
		for scanner.Scan() {
			lines++
		}
		closeErr := file.Close()
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if lines > item.MaxLines {
			t.Errorf(
				"%s grew to %d lines; budget is %d. Move reusable workflow code into an SDK",
				item.Path,
				lines,
				item.MaxLines,
			)
		}
	}
}

func TestExampleModsDeclareAdvisoryProfileUntilDurabilityIsProven(t *testing.T) {
	for _, path := range []string{
		"../examples/mods/fabric-rin-npc/README.md",
		"../examples/mods/fabric-rin-npc/README.zh-CN.md",
		"../examples/mods/bepinex-rin-npc/README.md",
		"../examples/mods/bepinex-rin-npc/README.zh-CN.md",
		"../examples/mods/luanti-rin-npc/README.md",
		"../examples/mods/luanti-rin-npc/README.zh-CN.md",
	} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, required := range []string{
			"Host capability profile",
			"`advisory`",
			"host-capability-profiles",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s does not declare the current host profile via %q", path, required)
			}
		}
	}
}


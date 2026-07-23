package compat_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBilingualDocumentationPairs(t *testing.T) {
	pairs := [][2]string{
		{"../README.en.md", "../README.md"},
		{"../ROADMAP.en.md", "../ROADMAP.md"},
		{"../SECURITY.en.md", "../SECURITY.md"},
		{"../docs/README.md", "../docs/README.zh-CN.md"},
		{"../docs/architecture.md", "../docs/architecture.zh-CN.md"},
		{"../docs/game-adapters.md", "../docs/game-adapters.zh-CN.md"},
		{"../docs/living-worlds-v0.5-plan.md", "../docs/living-worlds-v0.5-plan.zh-CN.md"},
		{"../docs/model-policy.md", "../docs/model-policy.zh-CN.md"},
		{"../docs/protocol-v1.md", "../docs/protocol-v1.zh-CN.md"},
		{"../docs/rpg-events.md", "../docs/rpg-events.zh-CN.md"},
		{"../docs/sdk-and-mods.md", "../docs/sdk-and-mods.zh-CN.md"},
		{"../sdk/README.md", "../sdk/README.zh-CN.md"},
		{"../sdk/python/README.md", "../sdk/python/README.zh-CN.md"},
		{"../sdk/javascript/README.md", "../sdk/javascript/README.zh-CN.md"},
		{"../sdk/csharp/README.md", "../sdk/csharp/README.zh-CN.md"},
		{"../sdk/java/README.md", "../sdk/java/README.zh-CN.md"},
		{"../sdk/lua/README.md", "../sdk/lua/README.zh-CN.md"},
		{"../examples/mods/fabric-rin-npc/README.md", "../examples/mods/fabric-rin-npc/README.zh-CN.md"},
		{"../examples/mods/bepinex-rin-npc/README.md", "../examples/mods/bepinex-rin-npc/README.zh-CN.md"},
		{"../examples/mods/luanti-rin-npc/README.md", "../examples/mods/luanti-rin-npc/README.zh-CN.md"},
	}

	for _, pair := range pairs {
		for _, path := range pair {
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				continue
			}
			text := string(payload)
			if !strings.Contains(text, "[English]") || !strings.Contains(text, "[简体中文]") {
				t.Errorf("%s is missing the bilingual navigation", path)
			}
		}
	}
}

func TestMarkdownLocalLinksResolve(t *testing.T) {
	linkPattern := regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "bin", "obj":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}

		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range linkPattern.FindAllStringSubmatch(string(payload), -1) {
			target := strings.Trim(strings.TrimSpace(match[1]), "<>")
			if target == "" || strings.HasPrefix(target, "#") ||
				strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") ||
				strings.HasPrefix(target, "mailto:") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			target = strings.SplitN(target, "?", 2)[0]
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
			if _, statErr := os.Stat(resolved); statErr != nil {
				t.Errorf("%s links to missing local target %s", path, target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMITLicenseMetadata(t *testing.T) {
	required := map[string]string{
		"../LICENSE":                                 "MIT License",
		"../sdk/python/pyproject.toml":               `license = {text = "MIT"}`,
		"../sdk/javascript/package.json":             `"license": "MIT"`,
		"../sdk/csharp/Rin.Client/Rin.Client.csproj": "<PackageLicenseExpression>MIT</PackageLicenseExpression>",
		"../examples/mods/fabric-rin-npc/src/main/resources/fabric.mod.json": `"license": "MIT"`,
	}
	for path, marker := range required {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !strings.Contains(string(payload), marker) {
			t.Errorf("%s is missing MIT metadata %q", path, marker)
		}
	}
}

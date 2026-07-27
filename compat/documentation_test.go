package compat_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestBilingualDocumentationPairs(t *testing.T) {
	pairs := [][2]string{
		{"../CHANGELOG.md", "../CHANGELOG.zh-CN.md"},
		{"../README.en.md", "../README.md"},
		{"../ROADMAP.en.md", "../ROADMAP.md"},
		{"../SECURITY.en.md", "../SECURITY.md"},
		{"../docs/README.md", "../docs/README.zh-CN.md"},
		{"../docs/action-lifecycle.md", "../docs/action-lifecycle.zh-CN.md"},
		{"../docs/architecture.md", "../docs/architecture.zh-CN.md"},
		{"../docs/compatibility.md", "../docs/compatibility.zh-CN.md"},
		{"../docs/game-adapters.md", "../docs/game-adapters.zh-CN.md"},
		{"../docs/host-contract.md", "../docs/host-contract.zh-CN.md"},
		{"../docs/host-durability.md", "../docs/host-durability.zh-CN.md"},
		{"../docs/host-sdk.md", "../docs/host-sdk.zh-CN.md"},
		{"../docs/model-policy.md", "../docs/model-policy.zh-CN.md"},
		{"../docs/open-spiel-validation.md", "../docs/open-spiel-validation.zh-CN.md"},
		{"../docs/optional-extensions.md", "../docs/optional-extensions.zh-CN.md"},
		{"../docs/host-scaffolding.md", "../docs/host-scaffolding.zh-CN.md"},
		{"../docs/host-integration-validation.md", "../docs/host-integration-validation.zh-CN.md"},
		{"../docs/long-session-validation.md", "../docs/long-session-validation.zh-CN.md"},
		{"../docs/protocol-v2.md", "../docs/protocol-v2.zh-CN.md"},
		{"../docs/release-guide.md", "../docs/release-guide.zh-CN.md"},
		{"../docs/rpg-events.md", "../docs/rpg-events.zh-CN.md"},
		{"../docs/sdk-and-mods.md", "../docs/sdk-and-mods.zh-CN.md"},
		{"../examples/native-host/README.md", "../examples/native-host/README.zh-CN.md"},
		{"../sdk/README.md", "../sdk/README.zh-CN.md"},
		{"../sdk/python/README.md", "../sdk/python/README.zh-CN.md"},
		{"../sdk/javascript/README.md", "../sdk/javascript/README.zh-CN.md"},
		{"../sdk/csharp/README.md", "../sdk/csharp/README.zh-CN.md"},
		{"../sdk/java/README.md", "../sdk/java/README.zh-CN.md"},
		{"../sdk/lua/README.md", "../sdk/lua/README.zh-CN.md"},
		{"../examples/README.md", "../examples/README.zh-CN.md"},
		{"../examples/godot/README.md", "../examples/godot/README.zh-CN.md"},
		{"../examples/unity/README.md", "../examples/unity/README.zh-CN.md"},
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
			if !strings.Contains(text, "[English]") ||
				!strings.Contains(text, "[简体中文]") {
				t.Errorf("%s is missing bilingual navigation", path)
			}
		}
	}
}

func TestCurrentDocumentationMatchesGeneratedContractIdentity(t *testing.T) {
	status := protocol.ContractReleaseStatus
	statusLabel := strings.ToUpper(status[:1]) + status[1:]
	for _, path := range []string{
		"../README.en.md",
		"../README.md",
		"../ROADMAP.en.md",
		"../ROADMAP.md",
		"../SECURITY.en.md",
		"../SECURITY.md",
		"../docs/README.md",
		"../docs/README.zh-CN.md",
		"../docs/compatibility.md",
		"../docs/compatibility.zh-CN.md",
		"../docs/protocol-v2.md",
		"../docs/protocol-v2.zh-CN.md",
		"../docs/release-guide.md",
		"../docs/release-guide.zh-CN.md",
	} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		if !strings.Contains(text, "`"+protocol.ContractReleaseVersion+"`") ||
			!strings.Contains(text, statusLabel) {
			t.Errorf(
				"%s does not identify %s as %s",
				path,
				protocol.ContractReleaseVersion,
				statusLabel,
			)
		}
	}
}

func TestCurrentGuidesDoNotTeachRemovedProtocol(t *testing.T) {
	roots := []string{
		"../README.md",
		"../README.en.md",
		"../ROADMAP.md",
		"../ROADMAP.en.md",
		"../SECURITY.md",
		"../SECURITY.en.md",
		"../docs",
		"../sdk",
		"../examples",
	}
	forbidden := []string{
		"rin.protocol/v1",
		"protocol-v1.md",
		"outcome-reporting-v1",
		"ActionSpec",
		"CommitRequest",
		"committable",
		"/v2/action/commit",
	}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{root}
		if info.IsDir() {
			paths = nil
			err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.IsDir() && strings.HasSuffix(path, ".md") {
					paths = append(paths, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range paths {
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range forbidden {
				if strings.Contains(string(payload), marker) {
					t.Errorf("%s still teaches removed protocol marker %q", path, marker)
				}
			}
		}
	}
}

func TestRelativeDocumentationLinksResolve(t *testing.T) {
	link := regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	for _, root := range []string{
		"../README.md",
		"../README.en.md",
		"../ROADMAP.md",
		"../ROADMAP.en.md",
		"../SECURITY.md",
		"../SECURITY.en.md",
		"../docs",
		"../sdk",
		"../examples",
	} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".md") ||
				filepath.Base(path) == "CHANGELOG.md" ||
				filepath.Base(path) == "CHANGELOG.zh-CN.md" {
				return nil
			}
			payload, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, match := range link.FindAllStringSubmatch(string(payload), -1) {
				target := strings.TrimSpace(match[1])
				if target == "" || strings.HasPrefix(target, "#") ||
					strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				target = strings.SplitN(target, "#", 2)[0]
				target = strings.Trim(target, "<>")
				if _, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(target))); err != nil {
					t.Errorf("%s has broken relative link %q", path, match[1])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRealHostValidationLimitsRemainExplicit(t *testing.T) {
	required := map[string][]string{
		"../docs/host-integration-validation.md": {
			"Minecraft Dedicated Server",
			"BepInEx 6 as bleeding-edge/unreleased",
			"Luanti headless server",
			"Unity Editor package import",
			"Mono and IL2CPP Players",
			"at least two hours or 1,000 turns",
			"`advisory`",
		},
		"../docs/host-integration-validation.zh-CN.md": {
			"Minecraft Dedicated Server",
			"BepInEx 6 视为 Bleeding-edge/未正式发布",
			"Luanti Headless Server",
			"Unity Editor Package 导入",
			"Windows Mono 与 IL2CPP Player",
			"至少两小时或 1,000 Turn",
			"`advisory`",
		},
		"../docs/game-adapters.md": {
			"macOS, Linux, and Windows",
			"licensed",
			"vision",
			"TTS consumes approved dialogue",
		},
		"../docs/game-adapters.zh-CN.md": {
			"macOS、Linux、Windows",
			"Unity",
			"Vision Model",
			"TTS",
		},
	}
	for path, markers := range required {
		assertSourceMarkers(t, path, markers, nil)
	}
}

package compat_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
		{"../docs/internal-agent-runtime.md", "../docs/internal-agent-runtime.zh-CN.md"},
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

func TestOpenAPIIdentifierHistoryVersionMatchesRuntime(t *testing.T) {
	payload, err := os.ReadFile("../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	var historySchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(
		document.Components.Schemas["IdentifierHistoryState"],
		&historySchema,
	); err != nil {
		t.Fatalf("decode OpenAPI Identifier History schema: %v", err)
	}
	var versionSchema struct {
		Const string `json:"const"`
	}
	if err := json.Unmarshal(
		historySchema.Properties["version"],
		&versionSchema,
	); err != nil {
		t.Fatalf("decode OpenAPI Identifier History version: %v", err)
	}
	version := versionSchema.Const
	if version != protocol.IdentifierHistoryVersion {
		t.Fatalf(
			"OpenAPI Identifier History version = %q, Runtime = %q",
			version,
			protocol.IdentifierHistoryVersion,
		)
	}
	for _, path := range []string{
		"../docs/compatibility.md",
		"../docs/compatibility.zh-CN.md",
		"../docs/architecture.md",
		"../docs/architecture.zh-CN.md",
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(
			string(content),
			"`"+protocol.IdentifierHistoryVersion+"`",
		) {
			t.Errorf(
				"%s does not identify %s",
				path,
				protocol.IdentifierHistoryVersion,
			)
		}
	}
}

func TestCurrentDocumentationEvidenceMatchesRepository(t *testing.T) {
	routesPayload, err := os.ReadFile("../sdk/conformance/routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var routes struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(routesPayload, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes.Operations) == 0 {
		t.Fatal("generated route inventory is empty")
	}
	assertSourceMarkers(
		t,
		"../docs/sdk-and-mods.md",
		[]string{fmt.Sprintf("%d-route inventory", len(routes.Operations))},
		[]string{"20-route inventory"},
	)
	assertSourceMarkers(
		t,
		"../docs/sdk-and-mods.zh-CN.md",
		[]string{fmt.Sprintf("%d Route", len(routes.Operations))},
		[]string{"20 Route"},
	)

	assertSourceMarkers(
		t,
		"../.github/workflows/ci.yml",
		[]string{
			"go test ./examples/adapters/story ./examples/terminal-story",
			"go run ./examples/terminal-story",
		},
		[]string{"benchmark.js --iterations", "Install the playable JavaScript slice"},
	)
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

func TestDocumentedRepositoryGoRunTargetsExist(t *testing.T) {
	command := regexp.MustCompile(`(?m)^\s*go run (\./[^\s\\]+)`)
	for _, path := range []string{"../README.md", "../README.en.md"} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range command.FindAllStringSubmatch(string(payload), -1) {
			target := filepath.Join("..", filepath.FromSlash(match[1]))
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s documents missing go run target %q", path, match[1])
				continue
			}
			if tracked, checked := repositoryTracks(target); checked && !tracked {
				t.Errorf(
					"%s documents go run target %q hidden by an untracked local path",
					path,
					match[1],
				)
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
			if tracked, checked := repositoryTracks(path); checked && !tracked {
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
				resolved := filepath.Join(filepath.Dir(path), filepath.FromSlash(target))
				if _, err := os.Stat(resolved); err != nil {
					t.Errorf("%s has broken relative link %q", path, match[1])
					continue
				}
				if tracked, checked := repositoryTracks(resolved); checked && !tracked {
					t.Errorf(
						"%s has relative link %q hidden by an untracked local path",
						path,
						match[1],
					)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func repositoryTracks(path string) (tracked bool, checked bool) {
	root, err := filepath.Abs("..")
	if err != nil {
		return false, false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false, false
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, true
	}
	command := exec.Command(
		"git",
		"-C",
		root,
		"ls-files",
		"--",
		filepath.ToSlash(relative),
	)
	output, err := command.Output()
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(output)) != "", true
}

func TestRepositoryTrackingRejectsUntrackedLocalPath(t *testing.T) {
	path, err := os.MkdirTemp("..", ".rin-untracked-link-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove temporary link target: %v", err)
		}
	})
	tracked, checked := repositoryTracks(path)
	if !checked {
		t.Skip("Git tracking metadata is unavailable")
	}
	if tracked {
		t.Fatal("untracked local path reported as repository content")
	}
}

func TestRealHostValidationLimitsRemainExplicit(t *testing.T) {
	required := map[string][]string{
		"../docs/host-integration-validation.md": {
			"Minecraft Dedicated Server",
			"BepInEx 6 as bleeding-edge/unreleased",
			"Luanti headless server",
			"Windows CI uses",
			"locally on macOS",
			"Linux CI runs",
			"Unity Editor package import",
			"Mono and IL2CPP Players",
			"at least two hours or 1,000 turns",
			"`advisory`",
		},
		"../docs/host-integration-validation.zh-CN.md": {
			"Minecraft Dedicated Server",
			"BepInEx 6 视为 Bleeding-edge/未正式发布",
			"Luanti Headless Server",
			"Windows CI 使用",
			"本地 macOS 通过",
			"Linux CI 运行",
			"Unity Editor Package 导入",
			"Windows Mono 与 IL2CPP Player",
			"至少两小时或 1,000 Turn",
			"`advisory`",
		},
		"../docs/game-adapters.md": {
			"Linux CI",
			"Windows execution is not yet",
			"automated.",
			"licensed",
			"vision",
			"TTS consumes approved dialogue",
		},
		"../docs/game-adapters.zh-CN.md": {
			"Linux CI",
			"Windows 执行尚未自动化",
			"Unity",
			"Vision Model",
			"TTS",
		},
	}
	for path, markers := range required {
		assertSourceMarkers(t, path, markers, nil)
	}
}

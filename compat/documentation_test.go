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
		{"../docs/model-policy.md", "../docs/model-policy.zh-CN.md"},
		{"../docs/outcome-reporting.md", "../docs/outcome-reporting.zh-CN.md"},
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

func TestPublicDocsUseOutcomeReportingSemantics(t *testing.T) {
	required := map[string][]string{
		"../docs/outcome-reporting.md": {
			"sole authority for world facts",
			"must never apply the action again",
			"proposal_base_mismatch",
			"observed_tick",
			"updated_tick",
			"progress_accumulator",
			"status_explicit",
			"status_updated_tick",
			"status_source_event_id",
			"outcome_event_id",
			"arbitration-v1",
			"BatchCommitRequest.tick",
			"unhandled saved Attempt",
		},
		"../docs/outcome-reporting.zh-CN.md": {
			"世界事实的唯一权威",
			"不得重新应用动作",
			"proposal_base_mismatch",
			"observed_tick",
			"updated_tick",
			"progress_accumulator",
			"status_explicit",
			"status_updated_tick",
			"status_source_event_id",
			"outcome_event_id",
			"arbitration-v1",
			"BatchCommitRequest.tick",
			"处理的存档 Attempt 能恢复",
		},
		"../docs/protocol-v1.md": {
			"job.error.code",
			"proposal_outcome_unknown",
			"Job is terminal, this code",
			"exact same `request_id` and payload",
			"two durable recovery states",
		},
		"../docs/protocol-v1.zh-CN.md": {
			"job.error.code",
			"proposal_outcome_unknown",
			"终态",
			"完全相同的",
			"两种持久恢复状态",
		},
		"../docs/game-adapters.md": {
			"`unresolved`",
			"`rin_proposal_attempt(request_id)`",
			"`rin_resume_proposal`",
			"positively confirmed `not_found`",
			"unconfigured example intentionally remains disabled",
			"tick high-water",
		},
		"../docs/game-adapters.zh-CN.md": {
			"`unresolved`",
			"`rin_proposal_attempt(request_id)`",
			"`rin_resume_proposal`",
			"确实 `not_found`",
			"未配置时示例会有意",
			"tick 高水位",
		},
	}
	for path, fragments := range required {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing outcome-reporting rule %q", path, fragment)
			}
		}
	}

	optInDocs := []string{
		"../README.md",
		"../README.en.md",
		"../docs/architecture.md",
		"../docs/architecture.zh-CN.md",
		"../docs/game-adapters.md",
		"../docs/game-adapters.zh-CN.md",
		"../docs/outcome-reporting.md",
		"../docs/outcome-reporting.zh-CN.md",
		"../docs/protocol-v1.md",
		"../docs/protocol-v1.zh-CN.md",
		"../docs/rpg-events.md",
		"../docs/rpg-events.zh-CN.md",
		"../docs/sdk-and-mods.md",
		"../docs/sdk-and-mods.zh-CN.md",
		"../sdk/README.md",
		"../sdk/README.zh-CN.md",
	}
	for _, path := range optInDocs {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), "outcome-reporting-v1") {
			t.Errorf("%s does not identify the outcome semantics as an explicit feature", path)
		}
	}

	prohibited := map[string]string{
		"../README.md":                   "游戏验证并调用 `commit` 后才生效",
		"../README.en.md":                "It takes effect only after the game validates it and calls",
		"../docs/protocol-v1.zh-CN.md":   "`status: pending`：必须 commit 才生效",
		"../docs/protocol-v1.md":         "the proposal has no effect until committed",
		"../docs/game-adapters.zh-CN.md": "先应用、后提交流程",
		"../docs/game-adapters.md":       "apply-before-commit flow",
	}
	for path, phrase := range prohibited {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), phrase) {
			t.Errorf("%s retains obsolete commit-as-authorization wording %q", path, phrase)
		}
	}
}

func TestStateClosureDocumentationContract(t *testing.T) {
	required := map[string][]string{
		"../docs/protocol-v1.md": {
			"Actor Goals | 32",
			"Actor detailed Memories | 128",
			"Actor Beliefs / BeliefSets | 256",
			"Recall counts saturate at 1,000,000",
			"Retained Proposal and Arbitration tick fields are not upper-bounded",
			"Imported historical Receipt revisions become",
			"This permanent ledger is reconstructed from",
			"`coverage_complete=false`",
			"`identifier_history_conflict`",
			"`mutation_outcome_unknown`",
		},
		"../docs/protocol-v1.zh-CN.md": {
			"Actor Goals | 32",
			"Actor 详细 Memories | 128",
			"Actor Beliefs / BeliefSets | 256",
			"RecallCount 在 1,000,000 饱和",
			"State 中保留的 Proposal 与 Arbitration tick 不受",
			"导入的历史 Receipt revision 会在",
			"Identifier History 仍会保留 Request 与 Event",
			"`coverage_complete=false`",
			"`identifier_history_conflict`",
			"`mutation_outcome_unknown`",
		},
		"../docs/architecture.md": {
			"reducer or candidate-validation failure",
			"Policy calls receive isolated copies",
			"Receipt revisions are set to zero",
			"`identifier-history-v1`",
			"`mutation_outcome_unknown`",
			"Store operations for one Session must be linearizable",
			"`Load` must be read-after-write consistent",
		},
		"../docs/architecture.zh-CN.md": {
			"reducer 或候选校验失败",
			"Policy 调用收到 State、Actor 和请求的隔离副本",
			"历史 Receipt revision 设为 0",
			"`identifier-history-v1`",
			"`mutation_outcome_unknown`",
			"Store 操作必须可线性化",
			"`Load` 对 `Create` 与",
		},
	}
	for path, fragments := range required {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing state-closure rule %q", path, fragment)
			}
		}
	}

	prohibited := map[string][]string{
		"../docs/architecture.md": {
			"A failed transition therefore leaves both the event log",
			"Receipt revision metadata is rebased",
		},
		"../docs/architecture.zh-CN.md": {
			"失败的转换既不会改变事件日志",
		},
		"../docs/protocol-v1.md": {
			"persistent idempotency index described in the migration roadmap",
			"does not provide a permanent Event ID index",
			"does not scan the unbounded event log",
		},
		"../docs/protocol-v1.zh-CN.md": {
			"迁移路线中的持久幂等索引",
			"尚未提供超出这些投影的永久 Event ID",
			"不会在该 ID 的所有有界投影均被淘汰后扫描无界事件日志",
		},
	}
	for path, fragments := range prohibited {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if strings.Contains(string(payload), fragment) {
				t.Errorf("%s retains obsolete state-closure wording %q", path, fragment)
			}
		}
	}
}

func TestPublicDocumentationLanguage(t *testing.T) {
	required := map[string]string{
		"../README.en.md": "> Game-native agent runtime.",
		"../README.md":    "> 面向游戏的智能体运行时。",
	}
	for path, marker := range required {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !strings.Contains(string(payload), marker) {
			t.Errorf("%s is missing the canonical description %q", path, marker)
		}
	}

	prohibited := []string{
		"ai-galgame",
		"unsent-letters",
		"deferred by lock screen",
		"因锁屏",
	}
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
		text := strings.ToLower(string(payload))
		for _, phrase := range prohibited {
			if strings.Contains(text, phrase) {
				t.Errorf("%s contains consumer-specific or local wording %q", path, phrase)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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

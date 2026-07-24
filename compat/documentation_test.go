package compat_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestBilingualDocumentationPairs(t *testing.T) {
	pairs := [][2]string{
		{"../CHANGELOG.md", "../CHANGELOG.zh-CN.md"},
		{"../README.en.md", "../README.md"},
		{"../ROADMAP.en.md", "../ROADMAP.md"},
		{"../SECURITY.en.md", "../SECURITY.md"},
		{"../docs/README.md", "../docs/README.zh-CN.md"},
		{"../docs/architecture.md", "../docs/architecture.zh-CN.md"},
		{"../docs/compatibility.md", "../docs/compatibility.zh-CN.md"},
		{"../docs/game-adapters.md", "../docs/game-adapters.zh-CN.md"},
		{"../docs/migration-v0.6.md", "../docs/migration-v0.6.zh-CN.md"},
		{"../docs/model-policy.md", "../docs/model-policy.zh-CN.md"},
		{"../docs/outcome-reporting.md", "../docs/outcome-reporting.zh-CN.md"},
		{"../docs/protocol-v1.md", "../docs/protocol-v1.zh-CN.md"},
		{"../docs/release-guide.md", "../docs/release-guide.zh-CN.md"},
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

func TestReleaseDocumentationIdentity(t *testing.T) {
	paths := []string{
		"../CHANGELOG.md",
		"../CHANGELOG.zh-CN.md",
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
		"../docs/migration-v0.6.md",
		"../docs/migration-v0.6.zh-CN.md",
		"../docs/protocol-v1.md",
		"../docs/protocol-v1.zh-CN.md",
		"../docs/release-guide.md",
		"../docs/release-guide.zh-CN.md",
	}
	for _, path := range paths {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		if !strings.Contains(text, protocol.ContractReleaseVersion) {
			t.Errorf("%s is missing generated release version %q", path, protocol.ContractReleaseVersion)
		}
		if !strings.Contains(strings.ToLower(text), protocol.ContractReleaseStatus) {
			t.Errorf("%s is missing generated release status %q", path, protocol.ContractReleaseStatus)
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

	englishSnapshotDocs := []string{
		"../README.en.md",
		"../SECURITY.en.md",
		"../docs/architecture.md",
		"../docs/game-adapters.md",
		"../docs/outcome-reporting.md",
		"../docs/protocol-v1.md",
		"../docs/rpg-events.md",
		"../docs/sdk-and-mods.md",
		"../sdk/README.md",
	}
	for _, path := range englishSnapshotDocs {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{
			"`expected_binding`",
			"trusted",
			"opaque",
			"SHA-256",
			"16 MiB",
			"32 MiB",
			"`413 snapshot_too_large`",
			"currently provided",
		} {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing bilingual English Snapshot rule %q", path, fragment)
			}
		}
	}

	chineseSnapshotDocs := []string{
		"../README.md",
		"../SECURITY.md",
		"../docs/architecture.zh-CN.md",
		"../docs/game-adapters.zh-CN.md",
		"../docs/outcome-reporting.zh-CN.md",
		"../docs/protocol-v1.zh-CN.md",
		"../docs/rpg-events.zh-CN.md",
		"../docs/sdk-and-mods.zh-CN.md",
		"../sdk/README.zh-CN.md",
	}
	for _, path := range chineseSnapshotDocs {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{
			"`expected_binding`",
			"可信",
			"不透明",
			"SHA-256",
			"16 MiB",
			"32 MiB",
			"`413 snapshot_too_large`",
			"当前不提供",
		} {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing bilingual Chinese Snapshot rule %q", path, fragment)
			}
		}
	}

	obsoletePromiseDocs := []string{
		"../README.en.md",
		"../README.md",
		"../SECURITY.en.md",
		"../SECURITY.md",
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
	futureStreamingPromises := []*regexp.Regexp{
		regexp.MustCompile(`(?is)(planned|future|forthcoming|upcoming|roadmap|step\s*5|awaits?|will\s+(?:add|provide|implement|support)).{0,160}(streaming\s+snapshot|snapshot\s+streaming)`),
		regexp.MustCompile(`(?is)(streaming\s+snapshot|snapshot\s+streaming).{0,160}(planned|future|forthcoming|upcoming|roadmap|step\s*5|will\s+(?:be\s+)?(?:added|provided|implemented|supported))`),
		regexp.MustCompile(`(?s)(计划|未来|后续将|路线图|第\s*5\s*步|Step\s*5|等待.{0,20}提供|将(?:会)?(?:提供|实现|支持)).{0,100}(流式\s*Snapshot|Snapshot\s*流式)`),
		regexp.MustCompile(`(?s)(流式\s*Snapshot|Snapshot\s*流式).{0,100}(计划|未来|路线图|Step\s*5|将(?:会)?(?:提供|实现|支持))`),
	}
	for _, path := range obsoletePromiseDocs {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range futureStreamingPromises {
			if match := pattern.Find(payload); match != nil {
				t.Errorf(
					"%s promises an unimplemented streaming Snapshot transport near %q",
					path,
					string(match),
				)
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

func TestSnapshotTransportAndTrustDocumentationContract(t *testing.T) {
	required := map[string][]string{
		"../README.en.md": {
			"Snapshot hashes are checksums",
			"trusted content manifest",
			"`expected_binding`",
			"16 MiB",
			"32 MiB",
			"`413 snapshot_too_large`",
			"No streaming Snapshot transport is currently provided",
		},
		"../README.md": {
			"Snapshot hash 是 checksum",
			"可信内容 manifest",
			"`expected_binding`",
			"16 MiB",
			"32 MiB",
			"`413 snapshot_too_large`",
			"当前不提供流式 Snapshot 传输",
		},
		"../docs/protocol-v1.md": {
			`"expected_binding": {`,
			"must come from the running game's trusted",
			"on an existing target all three must match",
			"not signatures",
			"do not authenticate provenance",
			"16 MiB",
			"32 MiB",
			"`413 snapshot_too_large`",
			"`413 body_too_large`",
			"still fits the configured request-body limit",
			"rejected during decoding first",
			"legacy four-field Restore request shape",
			"new-schema exact retry",
			"remain replayable",
			"Snapshot still fits the inline limit",
			"cannot be retransmitted through the inline API",
			"never silently truncated",
			"No streaming Snapshot transport is currently provided",
		},
		"../docs/protocol-v1.zh-CN.md": {
			`"expected_binding": {`,
			"运行中游戏的可信内容 manifest",
			"existing target 则要求三方全部匹配",
			"不是签名",
			"不验证来源真实性",
			"16 MiB",
			"32 MiB",
			"`413 snapshot_too_large`",
			"`413 body_too_large`",
			"仍处于配置的请求正文上限内",
			"解码阶段会优先返回",
			"旧四字段 Restore request shape",
			"新 schema exact retry",
			"正常重放",
			"Snapshot 仍在 inline 上限内时",
			"不能通过 inline API",
			"绝不会被静默截断",
			"当前不提供流式 Snapshot 传输",
		},
		"../docs/architecture.md": {
			"trusted, opaque serialized state",
			"running game's trusted content",
			"Complete compact inline Snapshot",
			"never truncated",
			"no streaming Snapshot transport is currently provided",
		},
		"../docs/architecture.zh-CN.md": {
			"Snapshot 是可信、",
			"不透明的序列化状态",
			"运行中游戏可信内容",
			"完整 inline Snapshot compact JSON",
			"绝不会截断",
			"当前不提供",
			"流式 Snapshot 传输",
		},
		"../sdk/README.md": {
			"trusted, opaque state",
			"running game's trusted content",
			"Every SDK defaults to a",
			"32 MiB response limit",
			"16 MiB",
			"No streaming Snapshot transport is currently provided",
		},
		"../sdk/README.zh-CN.md": {
			"可信、",
			"运行中游戏可信内容",
			"所有 SDK 默认响应上限为 32 MiB",
			"16 MiB",
			"当前不提供流式 Snapshot 传输",
		},
	}

	for path, fragments := range required {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing Snapshot transport/trust rule %q", path, fragment)
			}
		}
	}

	prohibited := map[string][]string{
		"../README.en.md": {
			"tampered or mismatched saves are",
		},
		"../README.md": {
			"篡改或串档会被拒绝",
		},
		"../sdk/README.md": {
			"default to a\n2 MiB response limit",
		},
		"../sdk/README.zh-CN.md": {
			"默认响应上限为\n2 MiB",
		},
		"../docs/protocol-v1.md": {
			"limits do not guarantee that every arbitrarily long Session Snapshot will fit",
		},
		"../docs/protocol-v1.zh-CN.md": {
			"当前 HTTP 与 SDK 上限不能保证任意长 Session",
		},
	}
	for path, fragments := range prohibited {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if strings.Contains(string(payload), fragment) {
				t.Errorf("%s retains obsolete Snapshot transport/trust wording %q", path, fragment)
			}
		}
	}
}

func TestFileStoreOperationsDocumentationContract(t *testing.T) {
	required := map[string][]string{
		"../docs/architecture.md": {
			"`.rin.lock`",
			"`events.idx`",
			rinruntime.CheckpointFormatVersion,
			rinruntime.ReducerProjectionVersion,
			"`RangeStore`",
			"`Head`",
			"`LoadRange`",
			"`CheckpointStore`",
			"`LoadCheckpoint`",
			"`SaveCheckpoint`",
			"same Store to implement both interfaces",
			"`retain_forever`",
			"two newest valid",
			"checkpoint files per Session",
			"two newest valid Snapshot",
			"not immutable by path",
			"same State revision/hash with newer",
			"`identifier_history_hash`",
			"`(*store.File).Close`",
			"`Engine.VerifyAll()`",
			"genesis-to-head",
			"derived cache",
			"projection version",
			"directory `fsync`",
			"`O(total event-log bytes)`",
			"best-effort asynchronously queues a",
			"`head revision / selected checkpoint revision >= 2`",
			"not be durable when the read returns",
			"trailing Timeline window",
			"NFS",
			"SMB",
			"FUSE",
			"cloud-synchronized",
			"`darwin`",
			"`linux`",
			"`ErrDataDirectoryLockUnsupported`",
			"every other GOOS",
			"fails closed",
		},
		"../docs/architecture.zh-CN.md": {
			"`.rin.lock`",
			"`events.idx`",
			rinruntime.CheckpointFormatVersion,
			rinruntime.ReducerProjectionVersion,
			"`RangeStore`",
			"`Head`",
			"`LoadRange`",
			"`CheckpointStore`",
			"`LoadCheckpoint`",
			"`SaveCheckpoint`",
			"同一个 Store 同时实现这两个接口",
			"`retain_forever`",
			"最近 2 个有效 checkpoint",
			"最近 2 个有效 Snapshot",
			"路径内容并非不可变",
			"Identifier History 更新的 Snapshot",
			"`identifier_history_hash`",
			"`(*store.File).Close`",
			"`Engine.VerifyAll()`",
			"genesis 到 head",
			"派生缓存",
			"projection version",
			"directory `fsync`",
			"`O(total event-log bytes)`",
			"异步排队一个恢复出的 head checkpoint",
			"`head revision / 所选 checkpoint revision >= 2`",
			"read 返回时它可能尚未持久化",
			"Timeline 尾部窗口",
			"NFS",
			"SMB",
			"FUSE",
			"云同步目录",
			"`darwin`",
			"`linux`",
			"`ErrDataDirectoryLockUnsupported`",
			"其他所有 GOOS",
			"fail closed",
		},
		"../README.en.md": {
			"non-blocking exclusive lock",
			"`(*store.File).Close()`",
			"`Engine.VerifyAll()`",
			"`retain_forever`",
			"two newest valid internal",
			"two newest valid public Snapshot",
			"queues an asynchronous checkpoint",
			"`head revision / selected checkpoint revision >= 2`",
			"durable when the read returns",
			"or from genesis when none is usable",
			"trailing window directly",
			"NFS",
			"SMB",
			"FUSE",
			"cloud-synchronized",
			"`darwin`",
			"`linux`",
			"`ErrDataDirectoryLockUnsupported`",
			"every other GOOS",
			"fails closed",
		},
		"../README.md": {
			"non-blocking exclusive lock",
			"`(*store.File).Close()`",
			"`Engine.VerifyAll()`",
			"`retain_forever`",
			"最近 2 个有效内部 checkpoint",
			"2 个有效公共 Snapshot",
			"异步排队一个恢复出的 head checkpoint",
			"`head revision / 所选 checkpoint revision >= 2`",
			"read 返回时它可能尚未",
			"若无可用",
			"checkpoint 则从 genesis 开始",
			"直接定位请求的尾部窗口",
			"NFS",
			"SMB",
			"FUSE",
			"云同步目录",
			"`darwin`",
			"`linux`",
			"`ErrDataDirectoryLockUnsupported`",
			"其他所有 GOOS",
			"fail closed",
		},
		"../SECURITY.en.md": {
			"non-blocking exclusive data-directory lock",
			"`(*store.File).Close()`",
			"`retain_forever`",
			"before JSON decoding",
			"invalid UTF-8",
			"unpaired JSON Unicode surrogates",
			"Before decoding a successful Provider",
			"JSON response, Rin strictly rejects",
			"A non-2xx Provider body is used only for bounded error",
			"it is not treated as content",
			"not an absolute",
			"local filesystem",
			"NFS",
			"SMB",
			"FUSE",
			"cloud-synchronized",
			"`darwin`",
			"`linux`",
			"`ErrDataDirectoryLockUnsupported`",
			"every other GOOS",
			"fails closed",
		},
		"../SECURITY.md": {
			"non-blocking exclusive lock",
			"`(*store.File).Close()`",
			"`retain_forever`",
			"JSON 解码前校验原始",
			"非法 UTF-8",
			"未配对 JSON Unicode Surrogate",
			"Provider JSON\n成功响应会在解码前严格拒绝",
			"非 2xx\nProvider Body 只用于有界错误分类",
			"不会被当成 Content",
			"不是针对",
			"故障的绝对持久性",
			"本地文件系统",
			"NFS",
			"SMB",
			"FUSE",
			"云同步目录",
			"`darwin`",
			"`linux`",
			"`ErrDataDirectoryLockUnsupported`",
			"其他所有 GOOS",
			"fail closed",
		},
		"../docs/protocol-v1.md": {
			"`current_revision`",
			"full-log rebuild",
			"derived caches",
			"mutation lock",
			"`Engine.VerifyAll()`",
			"best-effort asynchronously queues a checkpoint",
			"`head revision / selected checkpoint revision >= 2`",
			"durable when the successful Session read returns",
			"`500` | `store_load_failed`",
			"`500` | `replay_failed`",
		},
		"../docs/protocol-v1.zh-CN.md": {
			"`current_revision`",
			"全日志",
			"派生缓存",
			"mutation lock",
			"`Engine.VerifyAll()`",
			"异步排队一个恢复出的 head checkpoint",
			"`head revision / 所选 checkpoint revision >= 2`",
			"Session read 返回时它可能尚未持久化",
			"`500` | `store_load_failed`",
			"`500` | `replay_failed`",
		},
		"../docs/game-adapters.md": {
			"opens Session histories lazily",
			"`/health` proves process",
			"HTTP `404`",
			"`session_not_found`",
			"fresh Restore succeeds",
			"HTTP `500` `store_load_failed` and `replay_failed`",
			"never reinterpret",
		},
		"../docs/game-adapters.zh-CN.md": {
			"lazy 打开 Session 历史",
			"`/health` 只能证明进程可用",
			"HTTP `404`",
			"`session_not_found`",
			"fresh Restore 成功",
			"HTTP `500` `store_load_failed` 与 `replay_failed`",
			"绝不能解释成新周目",
		},
	}
	for path, fragments := range required {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing file-store operations rule %q", path, fragment)
			}
		}
	}

	prohibited := map[string][]string{
		"../README.en.md": {
			"The command verifies the log",
			"from a validated internal checkpoint plus its event tail",
		},
		"../README.md": {
			"会验证日志并只打印",
			"从校验后的内部 checkpoint 与 event tail",
		},
		"../SECURITY.en.md": {
			"validated after Go JSON decoding",
			"does not promise rejection of every raw non-UTF-8",
		},
		"../SECURITY.md": {
			"文本在 Go JSON 解码后校验",
			"不承诺拒绝每个",
		},
		"../docs/architecture.md": {
			"Startup fully replays and verifies the chain",
			"opening a data directory still verifies the entire event hash chain",
			"Public Snapshot files are immutable",
		},
		"../docs/architecture.zh-CN.md": {
			"启动时完整重放并验证",
			"打开数据目录时仍会验证全部事件 hash chain",
			"按 revision 与 State hash 命名且不可变",
		},
	}
	for path, fragments := range prohibited {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if strings.Contains(string(payload), fragment) {
				t.Errorf("%s retains obsolete eager-replay wording %q", path, fragment)
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

package cognition_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/experience"
	"github.com/sunrioa/rin/host"
)

func TestSQLiteMemoryPersistsFTSRecallAndPrivateVisibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	provider, err := cognition.OpenSQLiteMemoryProvider(path, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cognition.OpenSQLiteMemoryProvider(path, cognition.LocalMemoryConfig{}); !errors.Is(err, cognition.ErrMemoryStoreLocked) {
		t.Fatalf("second writer error = %v", err)
	}
	shared := sqliteMemory("memory.shared", cognition.MemoryActorSemantic, "", "The player prefers jasmine tea.")
	shared.Tags = []string{"preference"}
	shared.SubjectRefs = []string{"player"}
	if _, err := provider.Append(context.Background(), shared); err != nil {
		t.Fatal(err)
	}
	private := sqliteMemory("memory.private", cognition.MemoryControllerPrivate, "controller.one", "Private route hypothesis.")
	private.Provenance = cognition.MemoryProvenance{
		Source: cognition.MemorySourceModel, SourceID: "decision.one",
	}
	if _, err := provider.Append(context.Background(), private); err != nil {
		t.Fatal(err)
	}
	matches, err := provider.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one", ControllerID: "controller.one",
		Terms: []string{"jasmine"}, Tags: []string{"preference"},
		Now:    host.Timepoint{Clock: host.ClockStep, Value: 10},
		Budget: cognition.MemoryBudget{MaxRecords: 8, MaxCharacters: 2_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Record.MemoryID != "memory.shared" ||
		matches[0].Record.RecallCount != 1 {
		t.Fatalf("matches = %#v", matches)
	}
	hidden, err := provider.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one", ControllerID: "controller.two",
		Terms: []string{"hypothesis"}, Now: host.Timepoint{Clock: host.ClockStep, Value: 10},
		Budget: cognition.MemoryBudget{MaxRecords: 8, MaxCharacters: 2_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range hidden {
		if match.Record.MemoryID == "memory.private" {
			t.Fatalf("private memory leaked: %#v", hidden)
		}
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := cognition.OpenSQLiteMemoryProvider(path, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	snapshot, err := restored.Snapshot(context.Background())
	if err != nil || len(snapshot.Records) != 2 || snapshot.Records[0].RecallCount+snapshot.Records[1].RecallCount < 1 {
		t.Fatalf("restored snapshot = %#v, %v", snapshot, err)
	}
}

func TestSQLiteMemoryConsolidateForgetAndJSONLRoundTrip(t *testing.T) {
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	first := sqliteMemory("memory.one", cognition.MemoryActorEpisodic, "", "First shared event.")
	second := sqliteMemory("memory.two", cognition.MemoryActorEpisodic, "", "Second shared event.")
	second.CreatedAt.Value = 2
	for _, record := range []cognition.MemoryRecord{first, second} {
		if _, err := provider.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	summary := sqliteMemory("memory.summary", cognition.MemoryActorEpisodic, "", "The two events form one routine.")
	summary.CreatedAt.Value = 3
	if _, err := provider.Consolidate(context.Background(), cognition.MemoryConsolidation{
		Namespace: first.Namespace, SourceMemoryIDs: []string{"memory.one", "memory.two"},
		Summary: summary, Reason: "Merged repeated events.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Forget(context.Background(), cognition.MemoryForgetRequest{
		Namespace: first.Namespace, MemoryIDs: []string{"memory.summary"},
		Reason: "User requested removal.", At: host.Timepoint{Clock: host.ClockStep, Value: 4},
	}); err != nil {
		t.Fatal(err)
	}
	var exported bytes.Buffer
	if err := provider.ExportJSONL(context.Background(), &exported); err != nil {
		t.Fatal(err)
	}
	imported, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "imported.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer imported.Close()
	if err := imported.ImportJSONL(context.Background(), &exported); err != nil {
		t.Fatal(err)
	}
	snapshot, err := imported.Snapshot(context.Background())
	if err != nil || len(snapshot.Records) != 3 || len(snapshot.Tombstones) != 3 {
		t.Fatalf("imported snapshot = %#v, %v", snapshot, err)
	}
}

func TestSQLiteMemoryStoresCanonRefsAndExperienceCorrections(t *testing.T) {
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	record := sqliteMemory("memory.outcome", cognition.MemoryActorEpisodic, "", "The Host confirmed success.")
	record.Provenance = cognition.MemoryProvenance{
		Source: cognition.MemorySourceHostOutcome, SourceID: "operation.one", Authoritative: true,
	}
	record.CanonRef = &cognition.MemoryCanonRef{
		HostID: "host.one", WorldID: "world.one",
		Epoch:    host.Epoch{SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1},
		Sequence: 2, Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status: cognition.MemoryCanonCurrent,
	}
	if _, err := provider.Append(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	correction := experience.Correction{
		CorrectionID: "correction.one", OccurredAtUnixMillis: 10,
		Summary: "Try the nearer resource first.", RelatedEventID: "event.one",
	}
	if err := provider.AppendCorrection(context.Background(), "task.one", correction); err != nil {
		t.Fatal(err)
	}
	changed := correction
	changed.Summary = "A conflicting correction."
	if err := provider.AppendCorrection(context.Background(), "task.one", changed); !errors.Is(err, cognition.ErrProviderConflict) {
		t.Fatalf("changed correction error = %v", err)
	}
	corrections, err := provider.Corrections(context.Background(), "task.one")
	if err != nil || len(corrections) != 1 || corrections[0] != correction {
		t.Fatalf("corrections = %#v, %v", corrections, err)
	}
	snapshot, _ := provider.Snapshot(context.Background())
	if len(snapshot.Records) != 1 || snapshot.Records[0].CanonRef == nil ||
		snapshot.Records[0].CanonRef.Sequence != 2 {
		t.Fatalf("canon snapshot = %#v", snapshot)
	}
}

func TestSQLiteMemoryFiltersVisibilityBeforeBoundedRecall(t *testing.T) {
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	shared := sqliteMemory(
		"memory.shared.old", cognition.MemoryActorSemantic, "", "The shared promise remains visible.",
	)
	if _, err := provider.Append(context.Background(), shared); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 300; index++ {
		private := sqliteMemory(
			fmt.Sprintf("memory.private.%03d", index), cognition.MemoryControllerPrivate,
			"controller.other", fmt.Sprintf("Private recent note %03d.", index),
		)
		private.CreatedAt.Value = int64(index + 2)
		private.Provenance = cognition.MemoryProvenance{
			Source: cognition.MemorySourceModel, SourceID: fmt.Sprintf("decision.%03d", index),
		}
		if _, err := provider.Append(context.Background(), private); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := provider.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one", ControllerID: "controller.one",
		Now:    host.Timepoint{Clock: host.ClockStep, Value: 400},
		Budget: cognition.MemoryBudget{MaxRecords: 8, MaxCharacters: 2_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Record.MemoryID != shared.MemoryID {
		t.Fatalf("visible recall was crowded out: %#v", matches)
	}
}

func TestSQLiteMemoryTrigramRecallSupportsChineseAndJapanese(t *testing.T) {
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	contents := []struct {
		id      string
		content string
		term    string
	}{
		{"memory.chinese", "她在旧暗房照片背后写下日期。", "暗房照片"},
		{"memory.japanese", "彼女は屋上の約束を覚えている。", "屋上の約束"},
	}
	for index, fixture := range contents {
		record := sqliteMemory(fixture.id, cognition.MemoryActorEpisodic, "", fixture.content)
		record.CreatedAt.Value = int64(index + 1)
		if _, err := provider.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range contents {
		matches, err := provider.Retrieve(context.Background(), cognition.MemoryQuery{
			SessionID: "session.one", ActorID: "actor.one", Terms: []string{fixture.term},
			Now:    host.Timepoint{Clock: host.ClockStep, Value: 10},
			Budget: cognition.MemoryBudget{MaxRecords: 1, MaxCharacters: 2_000},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 || matches[0].Record.MemoryID != fixture.id ||
			!slices.Contains(matches[0].Reasons, "fts") {
			t.Fatalf("trigram recall for %q = %#v", fixture.term, matches)
		}
	}
}

func TestSQLiteMemoryMigratesAndRebuildsTrigramIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	provider, err := cognition.OpenSQLiteMemoryProvider(path, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	record := sqliteMemory(
		"memory.rebuild", cognition.MemoryActorEpisodic, "", "旧暗房照片仍保留着。",
	)
	if _, err := provider.Append(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TRIGGER memory_records_trigram_ai`,
		`DROP TRIGGER memory_records_trigram_ad`,
		`DROP TRIGGER memory_records_trigram_au`,
		`DROP TABLE memory_fts_trigram`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err = cognition.OpenSQLiteMemoryProvider(path, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	matches, err := provider.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one", Terms: []string{"暗房照片"},
		Now:    host.Timepoint{Clock: host.ClockStep, Value: 10},
		Budget: cognition.MemoryBudget{MaxRecords: 4, MaxCharacters: 2_000},
	})
	if err != nil || len(matches) == 0 || matches[0].Record.MemoryID != record.MemoryID {
		t.Fatalf("rebuilt trigram result = %#v, %v", matches, err)
	}
}

func TestSQLiteMemoryRecallEvaluation(t *testing.T) {
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	fixtures := []struct {
		id      string
		content string
		query   string
	}{
		{"memory.tea", "The player always orders jasmine tea after exploring.", "jasmine tea"},
		{"memory.bridge", "The safe ore route passes beneath the northern bridge.", "northern bridge"},
		{"memory.letter", "林晚晴把第五封信藏在暗房照片后面。", "第五封信"},
		{"memory.rooftop", "屋上の約束は文化祭の翌日に交わされた。", "文化祭の翌日"},
	}
	for index, fixture := range fixtures {
		record := sqliteMemory(fixture.id, cognition.MemoryActorSemantic, "", fixture.content)
		record.CreatedAt.Value = int64(index + 1)
		if _, err := provider.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 32; index++ {
		record := sqliteMemory(
			fmt.Sprintf("memory.distractor.%02d", index), cognition.MemoryActorSemantic, "",
			fmt.Sprintf("Unrelated routine note number %02d.", index),
		)
		record.CreatedAt.Value = int64(index + 10)
		if _, err := provider.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	recalled, reciprocalRank := 0, 0.0
	for _, fixture := range fixtures {
		matches, err := provider.Retrieve(context.Background(), cognition.MemoryQuery{
			SessionID: "session.one", ActorID: "actor.one", Terms: []string{fixture.query},
			Now:    host.Timepoint{Clock: host.ClockStep, Value: 100},
			Budget: cognition.MemoryBudget{MaxRecords: 3, MaxCharacters: 2_000},
		})
		if err != nil {
			t.Fatal(err)
		}
		for rank, id := range memoryMatchIDs(matches) {
			if id == fixture.id {
				recalled++
				reciprocalRank += 1 / float64(rank+1)
				break
			}
		}
	}
	recallAtThree := float64(recalled) / float64(len(fixtures))
	mrr := reciprocalRank / float64(len(fixtures))
	t.Logf("local retrieval fixture: Recall@3=%.2f MRR=%.2f", recallAtThree, mrr)
	if recallAtThree != 1 || mrr != 1 {
		t.Fatalf("local retrieval quality Recall@3=%.2f MRR=%.2f", recallAtThree, mrr)
	}
}

func TestSQLiteMemoryRetrievalIsStableAndBounded(t *testing.T) {
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	for index := 0; index < 320; index++ {
		content := fmt.Sprintf("Routine memory %03d.", index)
		if index == 0 {
			content = "Very old single-character memory: 茶"
		}
		record := sqliteMemory(
			fmt.Sprintf("memory.bounded.%03d", index), cognition.MemoryActorEpisodic, "", content,
		)
		record.CreatedAt.Value = int64(index + 1)
		if _, err := provider.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	query := cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one", Terms: []string{"茶"},
		Now:    host.Timepoint{Clock: host.ClockStep, Value: 400},
		Budget: cognition.MemoryBudget{MaxRecords: 8, MaxCharacters: 2_000},
	}
	first, err := provider.Retrieve(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Retrieve(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) > int(query.Budget.MaxRecords) ||
		!slices.Equal(memoryMatchIDs(first), memoryMatchIDs(second)) {
		t.Fatalf("short bounded retrieval changed or exceeded its budget: first=%v second=%v",
			memoryMatchIDs(first), memoryMatchIDs(second))
	}
}

func TestSQLiteMemoryRetrievalLatencySample(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("wall-clock latency guard is not meaningful with the race detector enabled")
	}
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	for index := 0; index < 500; index++ {
		record := sqliteMemory(
			fmt.Sprintf("memory.perf.%03d", index), cognition.MemoryActorEpisodic, "",
			fmt.Sprintf("Resource route %03d passes the old bridge and storage room.", index),
		)
		record.CreatedAt.Value = int64(index + 1)
		if _, err := provider.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	query := cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one", Terms: []string{"old bridge"},
		Now:    host.Timepoint{Clock: host.ClockStep, Value: 600},
		Budget: cognition.MemoryBudget{MaxRecords: 16, MaxCharacters: 6_000},
	}
	durations := make([]time.Duration, 40)
	for index := range durations {
		started := time.Now()
		if _, err := provider.Retrieve(context.Background(), query); err != nil {
			t.Fatal(err)
		}
		durations[index] = time.Since(started)
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	p50, p95 := durations[len(durations)/2], durations[len(durations)*95/100]
	t.Logf("SQLite local retrieval sample: p50=%s p95=%s", p50, p95)
	if p95 > 250*time.Millisecond {
		t.Fatalf("local retrieval p95 %s exceeds regression guard", p95)
	}
}

func sqliteMemory(
	id string,
	domain cognition.MemoryDomain,
	controller string,
	content string,
) cognition.MemoryRecord {
	return cognition.MemoryRecord{
		MemoryID: id,
		Namespace: cognition.MemoryNamespace{
			SessionID: "session.one", ActorID: "actor.one",
			ControllerID: controller, Domain: domain,
		},
		Content: content,
		Provenance: cognition.MemoryProvenance{
			Source: cognition.MemorySourcePlayer, SourceID: "player.one",
		},
		Confidence: 0.8, Importance: 0.7,
		CreatedAt: host.Timepoint{Clock: host.ClockStep, Value: 1},
	}
}

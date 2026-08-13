package cognition_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

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

func TestSQLiteMemoryImportsLegacySnapshotOnce(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "memory.json")
	legacy, err := cognition.OpenFileMemoryProvider(legacyPath, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	record := sqliteMemory(
		"memory.legacy", cognition.MemoryActorSemantic, "", "A legacy preference.",
	)
	if _, err := legacy.Append(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(directory, "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := provider.Snapshot(context.Background())
	if err != nil || len(snapshot.Records) != 1 || snapshot.Records[0].MemoryID != record.MemoryID {
		t.Fatalf("migrated snapshot = %#v, %v", snapshot, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, err = cognition.OpenFileMemoryProvider(legacyPath, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	second := sqliteMemory(
		"memory.legacy.later", cognition.MemoryActorSemantic, "", "A later legacy write.",
	)
	if _, err := legacy.Append(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err = cognition.OpenSQLiteMemoryProvider(
		filepath.Join(directory, "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	snapshot, err = provider.Snapshot(context.Background())
	if err != nil || len(snapshot.Records) != 1 || snapshot.Records[0].MemoryID != record.MemoryID {
		t.Fatalf("legacy data was re-imported = %#v, %v", snapshot, err)
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

package cognition_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sunrioa/rin/cognition"
)

func TestFileMemoryProviderPersistsRecallConsolidationAndForget(t *testing.T) {
	if !fileProviderLockingSupported() {
		t.Skip("memory store locking is not supported on this platform")
	}
	path := filepath.Join(t.TempDir(), "private", "memory.json")
	store, err := cognition.OpenFileMemoryProvider(path, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cognition.OpenFileMemoryProvider(
		path, cognition.LocalMemoryConfig{},
	); !errors.Is(err, cognition.ErrMemoryStoreLocked) {
		_ = store.Close()
		t.Fatalf("expected a second writer to be rejected, got %v", err)
	}
	namespace := actorMemoryNamespace()
	first := memoryRecord(
		"memory.file.first", namespace, "The player returned home.",
		cognition.MemorySourceHostOutcome, true, 1,
	)
	second := memoryRecord(
		"memory.file.second", namespace, "The player stored oak logs.",
		cognition.MemorySourceHostOutcome, true, 2,
	)
	for _, record := range []cognition.MemoryRecord{first, second} {
		if _, err := store.Append(context.Background(), record); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	beforeNoop, err := store.Snapshot(context.Background())
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), first); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	noMatches, err := store.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.other",
		Now: eventTime(3), Budget: cognition.MemoryBudget{MaxRecords: 8, MaxCharacters: 1_000},
	})
	if err != nil || len(noMatches) != 0 {
		_ = store.Close()
		t.Fatalf("no-match retrieval = %+v, %v", noMatches, err)
	}
	afterNoop, err := store.Snapshot(context.Background())
	if err != nil || afterNoop.Revision != beforeNoop.Revision {
		_ = store.Close()
		t.Fatalf("idempotent calls changed revision: before=%d after=%d err=%v", beforeNoop.Revision, afterNoop.Revision, err)
	}

	matches, err := store.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", Terms: []string{"player"},
		Now: eventTime(3), Budget: cognition.MemoryBudget{MaxRecords: 8, MaxCharacters: 1_000},
	})
	if err != nil || len(matches) != 2 || matches[0].Record.RecallCount != 1 {
		_ = store.Close()
		t.Fatalf("recall = %+v, %v", matches, err)
	}
	summary := memoryRecord(
		"memory.file.summary", namespace, "The player came home and stored oak logs.",
		cognition.MemorySourceHostOutcome, true, 4,
	)
	if _, err := store.Consolidate(context.Background(), cognition.MemoryConsolidation{
		Namespace: namespace, SourceMemoryIDs: []string{first.MemoryID, second.MemoryID},
		Summary: summary, Reason: "compact related events",
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := cognition.OpenFileMemoryProvider(path, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot(context.Background())
	if err != nil {
		_ = reopened.Close()
		t.Fatal(err)
	}
	if len(snapshot.Records) != 3 || len(snapshot.Tombstones) != 2 {
		_ = reopened.Close()
		t.Fatalf("durable memory snapshot = %+v", snapshot)
	}
	var recalled bool
	for _, record := range snapshot.Records {
		if record.MemoryID == first.MemoryID && record.RecallCount == 1 &&
			record.LastRecalledAt != nil && record.LastRecalledAt.Value == 3 {
			recalled = true
		}
	}
	if !recalled {
		_ = reopened.Close()
		t.Fatal("provider-owned recall metadata did not survive reopen")
	}
	if err := reopened.Forget(context.Background(), cognition.MemoryForgetRequest{
		Namespace: namespace, MemoryIDs: []string{summary.MemoryID},
		Reason: "player requested deletion", At: eventTime(5),
	}); err != nil {
		_ = reopened.Close()
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Snapshot(context.Background()); !errors.Is(err, cognition.ErrMemoryStoreClosed) {
		t.Fatalf("closed memory provider error = %v", err)
	}

	finalStore, err := cognition.OpenFileMemoryProvider(path, cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = finalStore.Close() })
	finalSnapshot, err := finalStore.Snapshot(context.Background())
	if err != nil || len(finalSnapshot.Tombstones) != 3 {
		t.Fatalf("forgotten memory did not survive reopen: %+v, %v", finalSnapshot, err)
	}
	if health := finalStore.Health(context.Background()); !health.Available || health.Degraded {
		t.Fatalf("healthy file provider = %+v", health)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("memory snapshot mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestFileMemoryProviderRejectsUnsafeSnapshots(t *testing.T) {
	if !fileProviderLockingSupported() {
		t.Skip("memory store locking is not supported on this platform")
	}
	for name, payload := range map[string]string{
		"malformed":     `{`,
		"unknown field": `{"revision":1,"records":[],"unknown":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.json")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := cognition.OpenFileMemoryProvider(
				path, cognition.LocalMemoryConfig{},
			); !errors.Is(err, cognition.ErrMemoryStorePersistence) {
				t.Fatalf("unsafe snapshot error = %v", err)
			}
		})
	}
	if runtime.GOOS == "windows" {
		return
	}
	t.Run("wide permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "memory.json")
		if err := os.WriteFile(path, []byte(`{"revision":1,"records":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := cognition.OpenFileMemoryProvider(
			path, cognition.LocalMemoryConfig{},
		); !errors.Is(err, cognition.ErrMemoryStorePersistence) {
			t.Fatalf("wide-permission snapshot error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.json")
		if err := os.WriteFile(target, []byte(`{"revision":1,"records":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "memory.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := cognition.OpenFileMemoryProvider(
			link, cognition.LocalMemoryConfig{},
		); err == nil {
			t.Fatal("symlink memory snapshot was accepted")
		}
	})
}

func fileProviderLockingSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows"
}

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestReadOnlyFileInspectsWithoutRepairingDerivedFiles(t *testing.T) {
	root := t.TempDir()
	const sessionID = "session.read-only"
	fileStore, engine := newStep5FileEngine(t, root, sessionID)
	step5Observe(t, engine, sessionID, 1)
	step5Observe(t, engine, sessionID, 2)
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}

	sessionDirectory := filepath.Join(root, "sessions", sessionID)
	if err := os.Remove(filepath.Join(sessionDirectory, "events.idx")); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := filepath.Glob(
		filepath.Join(sessionDirectory, "checkpoint-*"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range checkpoints {
		if err := os.Remove(checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	before := directoryNames(t, sessionDirectory)

	readOnly, err := OpenFileReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(readOnly).(rinruntime.CheckpointStore); ok {
		t.Fatal("read-only Store exposed Checkpoint writes")
	}
	if _, ok := any(readOnly).(rinruntime.LifecycleStore); ok {
		t.Fatal("read-only Store exposed lifecycle writes")
	}
	if _, ok := any(readOnly).(rinruntime.TransferStore); ok {
		t.Fatal("read-only Store exposed Transfer writes")
	}
	inspection, err := rinruntime.Open(readOnly, policy.Deterministic{})
	if err != nil {
		_ = readOnly.Close()
		t.Fatal(err)
	}
	state, err := inspection.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 3 {
		t.Fatalf("inspected revision = %d, want 3", state.Revision)
	}
	page, err := inspection.Timeline(protocol.TimelineRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		Limit:           8,
	})
	if err != nil || len(page.Entries) != 3 {
		t.Fatalf("read-only Timeline = %+v, %v", page, err)
	}
	if err := inspection.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	after := directoryNames(t, sessionDirectory)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only inspection changed files: before=%v after=%v", before, after)
	}
}

func TestReadOnlyFileRejectsWritesAndRequiresExistingStore(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenFileReadOnly(missing); err == nil {
		t.Fatal("read-only open created a missing data directory")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing data directory changed: %v", err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileReadOnly(root); err == nil {
		t.Fatal("read-only open accepted a directory without a Store lease file")
	}
	if _, err := os.Stat(filepath.Join(root, ".rin.lock")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("read-only open created a lease file: %v", err)
	}

	writer, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenFileReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.Create("session.denied", protocol.EventRecord{}); !errors.Is(
		err,
		ErrReadOnly,
	) {
		t.Fatalf("read-only Create error = %v", err)
	}
	if err := readOnly.Append("session.denied", protocol.EventRecord{}); !errors.Is(
		err,
		ErrReadOnly,
	) {
		t.Fatalf("read-only Append error = %v", err)
	}
	if err := readOnly.SaveSnapshot(
		"session.denied",
		protocol.Snapshot{},
	); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only SaveSnapshot error = %v", err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyFileUsesExclusiveOfflineLease(t *testing.T) {
	root := t.TempDir()
	writer, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := OpenFileReadOnly(root); !errors.Is(
		err,
		ErrDataDirectoryLocked,
	) {
		t.Fatalf("read-only open while writer active = %v", err)
	}
}

func TestFileStoresRejectSymlinkedStructuralPaths(t *testing.T) {
	for _, child := range []string{"sessions", "tombstones"} {
		t.Run(child, func(t *testing.T) {
			root := t.TempDir()
			fileStore, err := OpenFile(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := fileStore.Close(); err != nil {
				t.Fatal(err)
			}
			childPath := filepath.Join(root, child)
			if err := os.Remove(childPath); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, t.TempDir(), childPath)
			if opened, err := OpenFile(root); err == nil {
				_ = opened.Close()
				t.Fatalf("writer accepted symlinked %s", child)
			}
			if opened, err := OpenFileReadOnly(root); err == nil {
				_ = opened.Close()
				t.Fatalf("read-only view accepted symlinked %s", child)
			}
		})
	}

	t.Run("lock", func(t *testing.T) {
		root := t.TempDir()
		fileStore, err := OpenFile(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := fileStore.Close(); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(root, ".rin.lock")
		if err := os.Remove(lockPath); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "unrelated-lock")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, target, lockPath)
		if opened, err := OpenFile(root); err == nil {
			_ = opened.Close()
			t.Fatal("writer accepted a symlinked lock file")
		}
		if opened, err := OpenFileReadOnly(root); err == nil {
			_ = opened.Close()
			t.Fatal("read-only view accepted a symlinked lock file")
		}
	})
}

func TestReadOnlyFileRejectsCrossRootSessionsSymlink(t *testing.T) {
	sourceRoot := t.TempDir()
	source, err := OpenFile(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	aliasRoot := t.TempDir()
	alias, err := OpenFile(aliasRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := alias.Close(); err != nil {
		t.Fatal(err)
	}
	aliasSessions := filepath.Join(aliasRoot, "sessions")
	if err := os.Remove(aliasSessions); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(
		t,
		filepath.Join(sourceRoot, "sessions"),
		aliasSessions,
	)

	writer, err := OpenFile(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if opened, err := OpenFileReadOnly(aliasRoot); err == nil {
		_ = opened.Close()
		t.Fatal("read-only view bypassed the source data-directory lease")
	}
}

func symlinkOrSkip(t *testing.T, target string, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
}

func directoryNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

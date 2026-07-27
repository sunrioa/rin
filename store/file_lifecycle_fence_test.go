package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestFileArchiveExactRetryFencesPublishedMarker(t *testing.T) {
	fileStore, archive, _ := fileLifecycleFenceFixture(
		t,
		"session.archive-marker-fence",
	)
	defer fileStore.Close()

	directory := filepath.Join(fileStore.root, "sessions", archive.SessionID)
	marker := filepath.Join(directory, archiveFileName)
	sentinel := errors.New("injected archive directory fence failure")
	realSyncDir := fileStore.syncDir
	realSyncFile := fileStore.syncEventFile
	directorySyncs := 0
	markerSyncs := 0
	fileStore.syncDir = func(path string) error {
		if path == directory {
			directorySyncs++
			if directorySyncs == 1 {
				return sentinel
			}
		}
		return realSyncDir(path)
	}
	fileStore.syncEventFile = func(path string) error {
		if path == marker {
			markerSyncs++
		}
		return realSyncFile(path)
	}

	if _, _, err := fileStore.Archive(archive); !errors.Is(err, sentinel) {
		t.Fatalf("first Archive error = %v, want injected fence failure", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("archive marker was not published before fence failure: %v", err)
	}
	stored, duplicate, err := fileStore.Archive(archive)
	if err != nil {
		t.Fatalf("exact Archive retry did not fence the marker: %v", err)
	}
	if !duplicate || !archiveRecordsEqual(stored, archive) {
		t.Fatalf("unexpected Archive retry result: %+v duplicate=%t", stored, duplicate)
	}
	if markerSyncs != 1 || directorySyncs != 2 {
		t.Fatalf(
			"Archive retry syncs marker=%d directory=%d, want 1/2",
			markerSyncs,
			directorySyncs,
		)
	}
}

func TestFileDeleteExactRetryFencesPublishedTombstone(t *testing.T) {
	fileStore, archive, deletion := fileLifecycleFenceFixture(
		t,
		"session.delete-tombstone-fence",
	)
	defer fileStore.Close()
	if _, _, err := fileStore.Archive(archive); err != nil {
		t.Fatal(err)
	}

	tombstone := fileStore.tombstonePath(deletion.SessionID)
	tombstones := filepath.Dir(tombstone)
	sessionDirectory := filepath.Join(fileStore.root, "sessions", deletion.SessionID)
	sentinel := errors.New("injected tombstone directory fence failure")
	realSyncDir := fileStore.syncDir
	realSyncFile := fileStore.syncEventFile
	directorySyncs := 0
	tombstoneSyncs := 0
	fileStore.syncDir = func(path string) error {
		if path == tombstones {
			directorySyncs++
			if directorySyncs == 1 {
				return sentinel
			}
		}
		return realSyncDir(path)
	}
	fileStore.syncEventFile = func(path string) error {
		if path == tombstone {
			tombstoneSyncs++
		}
		return realSyncFile(path)
	}

	if _, _, err := fileStore.Delete(deletion); !errors.Is(err, sentinel) {
		t.Fatalf("first Delete error = %v, want injected fence failure", err)
	}
	if _, err := os.Stat(tombstone); err != nil {
		t.Fatalf("tombstone was not published before fence failure: %v", err)
	}
	if _, err := os.Stat(sessionDirectory); err != nil {
		t.Fatalf("Session was removed before the tombstone fence: %v", err)
	}
	stored, duplicate, err := fileStore.Delete(deletion)
	if err != nil {
		t.Fatalf("exact Delete retry did not finish retirement: %v", err)
	}
	if !duplicate || !deleteRecordsEqual(stored, deletion) {
		t.Fatalf("unexpected Delete retry result: %+v duplicate=%t", stored, duplicate)
	}
	if tombstoneSyncs != 1 || directorySyncs != 2 {
		t.Fatalf(
			"Delete retry syncs tombstone=%d directory=%d, want 1/2",
			tombstoneSyncs,
			directorySyncs,
		)
	}
	if _, err := os.Stat(sessionDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired Session remains visible: %v", err)
	}
}

func TestFileDeleteRetryFinishesUncertainSessionRename(t *testing.T) {
	fileStore, archive, deletion := fileLifecycleFenceFixture(
		t,
		"session.delete-rename-fence",
	)
	defer fileStore.Close()
	if _, _, err := fileStore.Archive(archive); err != nil {
		t.Fatal(err)
	}

	sessions := filepath.Join(fileStore.root, "sessions")
	sessionDirectory := filepath.Join(sessions, deletion.SessionID)
	sentinel := errors.New("injected deleted Session parent fence failure")
	realSyncDir := fileStore.syncDir
	sessionDirectorySyncs := 0
	fileStore.syncDir = func(path string) error {
		if path == sessions {
			sessionDirectorySyncs++
			if sessionDirectorySyncs == 1 {
				return sentinel
			}
		}
		return realSyncDir(path)
	}

	if _, _, err := fileStore.Delete(deletion); !errors.Is(err, sentinel) {
		t.Fatalf("first Delete error = %v, want injected fence failure", err)
	}
	if _, err := os.Stat(sessionDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Session rename did not occur before fence failure: %v", err)
	}
	pending, err := pendingDeletingDirectories(
		sessions,
		".deleting-"+deletion.SessionID+"-",
	)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending deletion directories = %v, err=%v", pending, err)
	}

	if _, duplicate, err := fileStore.Delete(deletion); err != nil || !duplicate {
		t.Fatalf("exact Delete retry = duplicate %t, error %v", duplicate, err)
	}
	pending, err = pendingDeletingDirectories(
		sessions,
		".deleting-"+deletion.SessionID+"-",
	)
	if err != nil || len(pending) != 0 {
		t.Fatalf("completed retry retained pending deletion %v, err=%v", pending, err)
	}
	if sessionDirectorySyncs != 3 {
		t.Fatalf(
			"Session directory fence attempts = %d, want 3",
			sessionDirectorySyncs,
		)
	}
}

func fileLifecycleFenceFixture(
	t *testing.T,
	sessionID string,
) (*File, rinruntime.ArchiveRecord, rinruntime.DeleteRecord) {
	t.Helper()
	_, frames, _ := fileTransferFixture(t, sessionID, 1)
	fileStore, err := OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	event := frames[0].Record
	if err := fileStore.Create(sessionID, event); err != nil {
		fileStore.Close()
		t.Fatal(err)
	}
	anchor := rinruntime.EventAnchor{
		Revision: event.Sequence,
		HeadHash: event.Hash,
	}
	archive := rinruntime.ArchiveRecord{
		SessionID:   sessionID,
		RequestID:   "archive." + sessionID,
		RequestHash: "archive-request-hash",
		ReceiptID:   "archive-receipt." + sessionID,
		ArchivedAt:  "2026-07-27T00:00:00Z",
		Anchor:      anchor,
	}
	deletion := rinruntime.DeleteRecord{
		FormatVersion:    "rin.session-tombstone/v1",
		SessionID:        sessionID,
		RequestID:        "delete." + sessionID,
		RequestHash:      "delete-request-hash",
		ReceiptID:        "delete-receipt." + sessionID,
		DeletedAt:        "2026-07-27T00:00:01Z",
		Anchor:           anchor,
		BindingHash:      "binding-hash",
		ArchiveReceiptID: archive.ReceiptID,
	}
	return fileStore, archive, deletion
}

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	rinruntime "github.com/sunrioa/rin/runtime"
)

const (
	archiveFileName = "archive.json"
	tombstoneSuffix = ".json"
)

func (s *File) tombstonePath(sessionID string) string {
	return filepath.Join(s.root, "tombstones", sessionID+tombstoneSuffix)
}

func (s *File) beginLifecycleSession(
	sessionID string,
) (string, func(), error) {
	directory, doneArtifact, err := s.beginArtifact(sessionID)
	if err != nil {
		return "", nil, err
	}
	unlockSession := s.lockSession(sessionID)
	return directory, func() {
		unlockSession()
		doneArtifact()
	}, nil
}

func (s *File) Lifecycle(
	sessionID string,
) (rinruntime.SessionLifecycle, error) {
	directory, done, err := s.beginArtifact(sessionID)
	if err != nil {
		return rinruntime.SessionLifecycle{}, err
	}
	defer done()
	if err := s.confirmArtifactSession(sessionID, directory); err != nil {
		return rinruntime.SessionLifecycle{}, err
	}
	var record rinruntime.ArchiveRecord
	if err := decodeJSONFile(
		filepath.Join(directory, archiveFileName),
		&record,
	); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rinruntime.SessionLifecycle{}, nil
		}
		return rinruntime.SessionLifecycle{}, err
	}
	if record.SessionID != sessionID {
		return rinruntime.SessionLifecycle{}, errors.New(
			"archive marker identifies a different Session",
		)
	}
	return rinruntime.SessionLifecycle{
		Archived:           true,
		ArchiveRequestID:   record.RequestID,
		ArchiveRequestHash: record.RequestHash,
		ArchiveReceiptID:   record.ReceiptID,
		ArchivedAt:         record.ArchivedAt,
		Anchor:             record.Anchor,
	}, nil
}

func (s *File) Stats(
	sessionID string,
) (rinruntime.StoreSessionStats, error) {
	directory, done, err := s.beginLifecycleSession(sessionID)
	if err != nil {
		return rinruntime.StoreSessionStats{}, err
	}
	defer done()
	if err := s.ensureSessionDurability(sessionID, directory); err != nil {
		return rinruntime.StoreSessionStats{}, err
	}
	file, err := openEventFile(directory, os.O_RDONLY)
	if err != nil {
		return rinruntime.StoreSessionStats{}, err
	}
	index, err := s.ensureEventIndex(sessionID, directory, file)
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return rinruntime.StoreSessionStats{}, err
	}
	result := rinruntime.StoreSessionStats{
		EventCount: uint64(len(index.entries)),
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return rinruntime.StoreSessionStats{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return rinruntime.StoreSessionStats{}, err
		}
		size := uint64(info.Size())
		switch {
		case entry.Name() == "events.jsonl":
			result.EventLogBytes += size
		case strings.HasPrefix(entry.Name(), "snapshot-"):
			result.SnapshotBytes += size
		case strings.HasPrefix(entry.Name(), "checkpoint-"):
			result.CheckpointBytes += size
		case strings.HasPrefix(entry.Name(), "events.idx"):
			result.IndexBytes += size
		default:
			result.OtherBytes += size
		}
	}
	return result, nil
}

func (s *File) Archive(
	record rinruntime.ArchiveRecord,
) (rinruntime.ArchiveRecord, bool, error) {
	directory, done, err := s.beginLifecycleSession(record.SessionID)
	if err != nil {
		return rinruntime.ArchiveRecord{}, false, err
	}
	defer done()
	if err := s.ensureSessionDurability(record.SessionID, directory); err != nil {
		return rinruntime.ArchiveRecord{}, false, err
	}
	if err := s.rejectDurabilityUncertainty(record.SessionID); err != nil {
		return rinruntime.ArchiveRecord{}, false, err
	}
	path := filepath.Join(directory, archiveFileName)
	var existing rinruntime.ArchiveRecord
	if err := decodeJSONFile(path, &existing); err == nil {
		if archiveRecordsEqual(existing, record) {
			return existing, true, nil
		}
		return rinruntime.ArchiveRecord{}, false, rinruntime.ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return rinruntime.ArchiveRecord{}, false, err
	}
	anchor, err := s.sessionAnchorLocked(record.SessionID, directory)
	if err != nil {
		return rinruntime.ArchiveRecord{}, false, err
	}
	if anchor != record.Anchor {
		return rinruntime.ArchiveRecord{}, false, rinruntime.ErrConflict
	}
	if err := s.writeJSONAtomically(
		directory,
		".archive-*.tmp",
		path,
		record,
		true,
	); err != nil {
		return rinruntime.ArchiveRecord{}, false, err
	}
	return record, false, nil
}

func (s *File) Delete(
	record rinruntime.DeleteRecord,
) (rinruntime.DeleteRecord, bool, error) {
	directory, done, err := s.beginLifecycleSession(record.SessionID)
	if err != nil {
		return rinruntime.DeleteRecord{}, false, err
	}
	defer done()
	tombstone := s.tombstonePath(record.SessionID)
	var existing rinruntime.DeleteRecord
	if err := decodeJSONFile(tombstone, &existing); err == nil {
		if !deleteRecordsEqual(existing, record) {
			return rinruntime.DeleteRecord{}, false, rinruntime.ErrRetired
		}
		if err := s.finishDeletedSession(directory); err != nil {
			return rinruntime.DeleteRecord{}, false, err
		}
		return existing, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return rinruntime.DeleteRecord{}, false, err
	}
	if err := s.ensureSessionDurability(record.SessionID, directory); err != nil {
		return rinruntime.DeleteRecord{}, false, err
	}
	var archive rinruntime.ArchiveRecord
	if err := decodeJSONFile(
		filepath.Join(directory, archiveFileName),
		&archive,
	); err != nil {
		return rinruntime.DeleteRecord{}, false, err
	}
	if archive.ReceiptID != record.ArchiveReceiptID ||
		archive.Anchor != record.Anchor {
		return rinruntime.DeleteRecord{}, false, rinruntime.ErrConflict
	}
	tombstones := filepath.Dir(tombstone)
	if err := s.writeJSONAtomically(
		tombstones,
		".tombstone-*.tmp",
		tombstone,
		record,
		true,
	); err != nil {
		return rinruntime.DeleteRecord{}, false, err
	}
	if err := s.finishDeletedSession(directory); err != nil {
		return rinruntime.DeleteRecord{}, false, err
	}
	s.clearSessionCaches(record.SessionID)
	return record, false, nil
}

func (s *File) sessionAnchorLocked(
	sessionID string,
	directory string,
) (rinruntime.EventAnchor, error) {
	file, err := openEventFile(directory, os.O_RDONLY)
	if err != nil {
		return rinruntime.EventAnchor{}, err
	}
	defer file.Close()
	index, err := s.ensureEventIndex(sessionID, directory, file)
	if err != nil {
		return rinruntime.EventAnchor{}, err
	}
	last, err := readIndexedEvent(file, index.entries[len(index.entries)-1])
	if err != nil {
		return rinruntime.EventAnchor{}, err
	}
	if err := verifyIndexedEvent(index.entries, len(index.entries)-1, last); err != nil {
		return rinruntime.EventAnchor{}, err
	}
	return rinruntime.EventAnchor{
		Revision: last.Sequence,
		HeadHash: last.Hash,
	}, nil
}

func (s *File) finishDeletedSession(directory string) error {
	if _, err := os.Stat(directory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	sessions := filepath.Dir(directory)
	deleting, err := os.MkdirTemp(
		sessions,
		".deleting-"+filepath.Base(directory)+"-*.tmp",
	)
	if err != nil {
		return err
	}
	if err := os.Remove(deleting); err != nil {
		return err
	}
	if err := renameDurably(directory, deleting); err != nil {
		return err
	}
	if err := s.syncDir(sessions); err != nil {
		return err
	}
	if err := os.RemoveAll(deleting); err != nil {
		return err
	}
	return s.syncDir(sessions)
}

func (s *File) clearSessionCaches(sessionID string) {
	s.indexesMu.Lock()
	delete(s.indexes, sessionID)
	s.indexesMu.Unlock()
	s.uncertainMu.Lock()
	delete(s.uncertainAppends, sessionID)
	s.uncertainMu.Unlock()
	s.durabilityMu.Lock()
	delete(s.durabilityConfirmed, sessionID)
	s.durabilityMu.Unlock()
}

func (s *File) finishPendingDeletions() error {
	tombstones := filepath.Join(s.root, "tombstones")
	entries, err := os.ReadDir(tombstones)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() ||
			!strings.HasSuffix(entry.Name(), tombstoneSuffix) {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), tombstoneSuffix)
		if !safeID.MatchString(sessionID) {
			return fmt.Errorf("unsafe tombstone name %q", entry.Name())
		}
		var record rinruntime.DeleteRecord
		if err := decodeJSONFile(
			filepath.Join(tombstones, entry.Name()),
			&record,
		); err != nil {
			return fmt.Errorf("read tombstone %s: %w", sessionID, err)
		}
		if record.SessionID != sessionID {
			return fmt.Errorf("tombstone %s identifies a different Session", sessionID)
		}
		if record.FormatVersion != "rin.session-tombstone/v1" {
			return fmt.Errorf("tombstone %s has an unsupported format", sessionID)
		}
		if err := s.finishDeletedSession(
			filepath.Join(s.root, "sessions", sessionID),
		); err != nil {
			return fmt.Errorf("finish deletion %s: %w", sessionID, err)
		}
	}
	return nil
}

package store

import (
	"encoding/json"

	rinruntime "github.com/sunrioa/rin/runtime"
)

func (s *Memory) Lifecycle(
	sessionID string,
) (rinruntime.SessionLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, retired := s.tombstones[sessionID]; retired {
		return rinruntime.SessionLifecycle{}, rinruntime.ErrRetired
	}
	if _, exists := s.events[sessionID]; !exists {
		return rinruntime.SessionLifecycle{}, rinruntime.ErrNotFound
	}
	archive, archived := s.archives[sessionID]
	return rinruntime.SessionLifecycle{
		Archived:           archived,
		ArchiveRequestID:   archive.RequestID,
		ArchiveRequestHash: archive.RequestHash,
		ArchiveReceiptID:   archive.ReceiptID,
		ArchivedAt:         archive.ArchivedAt,
		Anchor:             archive.Anchor,
	}, nil
}

func (s *Memory) Stats(sessionID string) (rinruntime.StoreSessionStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, exists := s.events[sessionID]
	if !exists {
		return rinruntime.StoreSessionStats{}, rinruntime.ErrNotFound
	}
	result := rinruntime.StoreSessionStats{EventCount: uint64(len(events))}
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return rinruntime.StoreSessionStats{}, err
		}
		result.EventLogBytes += uint64(len(encoded) + 1)
	}
	result.SnapshotBytes = encodedBytes(s.snapshots[sessionID])
	result.CheckpointBytes = encodedBytes(s.checkpoints[sessionID])
	if archive, ok := s.archives[sessionID]; ok {
		result.OtherBytes = encodedBytes(archive)
	}
	return result, nil
}

func (s *Memory) Archive(
	record rinruntime.ArchiveRecord,
) (rinruntime.ArchiveRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, retired := s.tombstones[record.SessionID]; retired {
		return rinruntime.ArchiveRecord{}, false, rinruntime.ErrRetired
	}
	events, exists := s.events[record.SessionID]
	if !exists {
		return rinruntime.ArchiveRecord{}, false, rinruntime.ErrNotFound
	}
	if existing, archived := s.archives[record.SessionID]; archived {
		if archiveRecordsEqual(existing, record) {
			return existing, true, nil
		}
		return rinruntime.ArchiveRecord{}, false, rinruntime.ErrConflict
	}
	tail := events[len(events)-1]
	if tail.Sequence != record.Anchor.Revision ||
		tail.Hash != record.Anchor.HeadHash {
		return rinruntime.ArchiveRecord{}, false, rinruntime.ErrConflict
	}
	s.archives[record.SessionID] = record
	return record, false, nil
}

func (s *Memory) Delete(
	record rinruntime.DeleteRecord,
) (rinruntime.DeleteRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, retired := s.tombstones[record.SessionID]; retired {
		if deleteRecordsEqual(existing, record) {
			return existing, true, nil
		}
		return rinruntime.DeleteRecord{}, false, rinruntime.ErrRetired
	}
	archive, archived := s.archives[record.SessionID]
	if !archived {
		if _, exists := s.events[record.SessionID]; !exists {
			return rinruntime.DeleteRecord{}, false, rinruntime.ErrNotFound
		}
		return rinruntime.DeleteRecord{}, false, rinruntime.ErrConflict
	}
	if archive.ReceiptID != record.ArchiveReceiptID ||
		archive.Anchor != record.Anchor {
		return rinruntime.DeleteRecord{}, false, rinruntime.ErrConflict
	}
	s.tombstones[record.SessionID] = record
	delete(s.events, record.SessionID)
	delete(s.snapshots, record.SessionID)
	delete(s.checkpoints, record.SessionID)
	delete(s.archives, record.SessionID)
	return record, false, nil
}

func encodedBytes(value any) uint64 {
	encoded, _ := json.Marshal(value)
	return uint64(len(encoded))
}

func archiveRecordsEqual(
	left rinruntime.ArchiveRecord,
	right rinruntime.ArchiveRecord,
) bool {
	return left.SessionID == right.SessionID &&
		left.RequestID == right.RequestID &&
		left.RequestHash == right.RequestHash &&
		left.ReceiptID == right.ReceiptID &&
		left.Anchor == right.Anchor
}

func deleteRecordsEqual(
	left rinruntime.DeleteRecord,
	right rinruntime.DeleteRecord,
) bool {
	return left.FormatVersion == right.FormatVersion &&
		left.SessionID == right.SessionID &&
		left.RequestID == right.RequestID &&
		left.RequestHash == right.RequestHash &&
		left.ReceiptID == right.ReceiptID &&
		left.Anchor == right.Anchor &&
		left.BindingHash == right.BindingHash &&
		left.ArchiveReceiptID == right.ArchiveReceiptID
}

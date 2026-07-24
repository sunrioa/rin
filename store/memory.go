package store

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

// Memory is useful for embedding Rin in tests and ephemeral game tools.
type Memory struct {
	mu          sync.Mutex
	events      map[string][]protocol.EventRecord
	snapshots   map[string][]protocol.Snapshot
	checkpoints map[string][]rinruntime.Checkpoint
}

func NewMemory() *Memory {
	return &Memory{
		events:      make(map[string][]protocol.EventRecord),
		snapshots:   make(map[string][]protocol.Snapshot),
		checkpoints: make(map[string][]rinruntime.Checkpoint),
	}
}

func (s *Memory) Create(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if events, exists := s.events[sessionID]; exists {
		if len(events) == 1 && rinruntime.EventRecordsExactlyEqual(events[0], event) {
			return rinruntime.VerifyEventRecord(0, "", event)
		}
		return rinruntime.ErrConflict
	}
	if err := rinruntime.VerifyEventRecord(0, "", event); err != nil {
		return err
	}
	s.events[sessionID] = []protocol.EventRecord{cloneEventRecord(event)}
	return nil
}

func (s *Memory) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, exists := s.events[sessionID]
	if !exists {
		return rinruntime.ErrNotFound
	}
	last := events[len(events)-1]
	if rinruntime.EventRecordsExactlyEqual(event, last) {
		return nil
	}
	if err := rinruntime.VerifyEventRecord(last.Sequence, last.Hash, event); err != nil {
		return rinruntime.ErrConflict
	}
	s.events[sessionID] = append(events, cloneEventRecord(event))
	return nil
}

func (s *Memory) Load(sessionID string) ([]protocol.EventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, exists := s.events[sessionID]
	if !exists {
		return nil, rinruntime.ErrNotFound
	}
	result := make([]protocol.EventRecord, len(events))
	for index, event := range events {
		result[index] = cloneEventRecord(event)
	}
	return result, nil
}

func (s *Memory) ListSessions() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, 0, len(s.events))
	for id := range s.events {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Memory) SaveSnapshot(sessionID string, snapshot protocol.Snapshot) error {
	cloned, err := cloneSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := rinruntime.ValidateSnapshot(cloned); err != nil {
		return err
	}
	if cloned.State.SessionID != sessionID {
		return errors.New("snapshot session id does not match destination")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[sessionID]; !exists {
		return rinruntime.ErrNotFound
	}
	snapshots := s.snapshots[sessionID]
	replaced := false
	for index := range snapshots {
		if snapshots[index].State.Revision == cloned.State.Revision &&
			snapshots[index].StateHash == cloned.StateHash &&
			snapshots[index].IdentifierHistoryHash == cloned.IdentifierHistoryHash {
			snapshots[index] = cloned
			replaced = true
			break
		}
	}
	if !replaced {
		snapshots = append(snapshots, cloned)
	}
	sort.Slice(snapshots, func(left, right int) bool {
		if snapshots[left].State.Revision != snapshots[right].State.Revision {
			return snapshots[left].State.Revision < snapshots[right].State.Revision
		}
		if snapshots[left].StateHash != snapshots[right].StateHash {
			return snapshots[left].StateHash < snapshots[right].StateHash
		}
		return snapshots[left].IdentifierHistoryHash <
			snapshots[right].IdentifierHistoryHash
	})
	if len(snapshots) > snapshotRetentionCount {
		snapshots = append([]protocol.Snapshot(nil), snapshots[len(snapshots)-snapshotRetentionCount:]...)
	}
	s.snapshots[sessionID] = snapshots
	return nil
}

func cloneSnapshot(snapshot protocol.Snapshot) (protocol.Snapshot, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	var cloned protocol.Snapshot
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return protocol.Snapshot{}, err
	}
	return cloned, nil
}

func (s *Memory) Head(sessionID string) (rinruntime.EventAnchor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, exists := s.events[sessionID]
	if !exists || len(events) == 0 {
		return rinruntime.EventAnchor{}, rinruntime.ErrNotFound
	}
	last := events[len(events)-1]
	return rinruntime.EventAnchor{Revision: last.Sequence, HeadHash: last.Hash}, nil
}

func (s *Memory) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rinruntime.EventPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		return rinruntime.EventPage{}, errors.New("event range limit must be positive")
	}
	events, exists := s.events[sessionID]
	if !exists || len(events) == 0 {
		return rinruntime.EventPage{}, rinruntime.ErrNotFound
	}
	head := uint64(len(events))
	if throughRevision == 0 {
		throughRevision = head
	}
	if afterRevision > head || throughRevision > head {
		return rinruntime.EventPage{}, rinruntime.ErrNotFound
	}
	if throughRevision <= afterRevision {
		return rinruntime.EventPage{Events: []protocol.EventRecord{}}, nil
	}
	endRevision := throughRevision
	if uint64(limit) < endRevision-afterRevision {
		endRevision = afterRevision + uint64(limit)
	}
	page := rinruntime.EventPage{
		Events:  make([]protocol.EventRecord, 0, int(endRevision-afterRevision)),
		HasMore: endRevision < throughRevision,
	}
	var previousRevision uint64
	var previousHash string
	if afterRevision > 0 {
		anchor := events[afterRevision-1]
		if afterRevision == 1 {
			if err := rinruntime.VerifyEventRecord(0, "", anchor); err != nil {
				return rinruntime.EventPage{}, err
			}
		} else {
			// Create and Append validate every write while holding mu, and the
			// private slice is never exposed. Verify only the immediate durable
			// adjacency needed to anchor this page; rescanning genesis for every
			// Timeline page would make pagination quadratic.
			predecessor := events[afterRevision-2]
			if err := rinruntime.VerifyEventRecord(
				predecessor.Sequence,
				predecessor.Hash,
				anchor,
			); err != nil {
				return rinruntime.EventPage{}, err
			}
		}
		if anchor.Sequence != afterRevision {
			return rinruntime.EventPage{}, rinruntime.ErrCorruptLog
		}
		previousRevision = anchor.Sequence
		previousHash = anchor.Hash
	}
	for position := afterRevision; position < endRevision; position++ {
		event := events[position]
		if err := rinruntime.VerifyEventRecord(previousRevision, previousHash, event); err != nil {
			return rinruntime.EventPage{}, err
		}
		page.Events = append(page.Events, cloneEventRecord(event))
		previousRevision = event.Sequence
		previousHash = event.Hash
	}
	return page, nil
}

func (s *Memory) SaveCheckpoint(sessionID string, checkpoint rinruntime.Checkpoint) error {
	s.mu.Lock()
	if _, exists := s.events[sessionID]; !exists {
		s.mu.Unlock()
		return rinruntime.ErrNotFound
	}
	s.mu.Unlock()

	cloned, err := cloneCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	if err := rinruntime.ValidateCheckpoint(cloned); err != nil {
		return err
	}
	if cloned.SessionID != sessionID {
		return errors.New("checkpoint session id does not match destination")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[sessionID]; !exists {
		return rinruntime.ErrNotFound
	}
	checkpoints := s.checkpoints[sessionID]
	replaced := false
	for index := range checkpoints {
		if checkpoints[index].Revision == cloned.Revision &&
			checkpoints[index].Checksum == cloned.Checksum {
			checkpoints[index] = cloned
			replaced = true
			break
		}
	}
	if !replaced {
		checkpoints = append(checkpoints, cloned)
	}
	sort.Slice(checkpoints, func(left, right int) bool {
		if checkpoints[left].Revision == checkpoints[right].Revision {
			return checkpoints[left].Checksum < checkpoints[right].Checksum
		}
		return checkpoints[left].Revision < checkpoints[right].Revision
	})
	if len(checkpoints) > checkpointRetentionCount {
		checkpoints = append(
			[]rinruntime.Checkpoint(nil),
			checkpoints[len(checkpoints)-checkpointRetentionCount:]...,
		)
	}
	s.checkpoints[sessionID] = checkpoints
	return nil
}

func (s *Memory) LoadCheckpoint(
	sessionID string,
	atOrBeforeRevision uint64,
) (rinruntime.Checkpoint, error) {
	s.mu.Lock()
	checkpoints := s.checkpoints[sessionID]
	for position := len(checkpoints) - 1; position >= 0; position-- {
		if checkpoints[position].Revision > atOrBeforeRevision {
			continue
		}
		checkpoint := checkpoints[position]
		s.mu.Unlock()
		return cloneCheckpoint(checkpoint)
	}
	s.mu.Unlock()
	return rinruntime.Checkpoint{}, rinruntime.ErrNotFound
}

func cloneCheckpoint(checkpoint rinruntime.Checkpoint) (rinruntime.Checkpoint, error) {
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return rinruntime.Checkpoint{}, err
	}
	var cloned rinruntime.Checkpoint
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return rinruntime.Checkpoint{}, err
	}
	return cloned, nil
}

func cloneEventRecord(event protocol.EventRecord) protocol.EventRecord {
	event.Data = append([]byte(nil), event.Data...)
	return event
}

package store

import (
	"sort"
	"sync"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

// Memory is useful for embedding Rin in tests and ephemeral game tools.
type Memory struct {
	mu        sync.Mutex
	events    map[string][]protocol.EventRecord
	snapshots map[string]protocol.Snapshot
}

func NewMemory() *Memory {
	return &Memory{
		events:    make(map[string][]protocol.EventRecord),
		snapshots: make(map[string]protocol.Snapshot),
	}
}

func (s *Memory) Create(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[sessionID]; exists {
		return rinruntime.ErrConflict
	}
	s.events[sessionID] = []protocol.EventRecord{event}
	return nil
}

func (s *Memory) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[sessionID]; !exists {
		return rinruntime.ErrNotFound
	}
	s.events[sessionID] = append(s.events[sessionID], event)
	return nil
}

func (s *Memory) Load(sessionID string) ([]protocol.EventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, exists := s.events[sessionID]
	if !exists {
		return nil, rinruntime.ErrNotFound
	}
	return append([]protocol.EventRecord(nil), events...), nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[sessionID]; !exists {
		return rinruntime.ErrNotFound
	}
	s.snapshots[sessionID] = snapshot
	return nil
}

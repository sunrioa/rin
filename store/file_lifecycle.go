package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	rinruntime "github.com/sunrioa/rin/runtime"
)

func (s *File) beginSession(sessionID string) (string, func(), error) {
	directory, err := s.sessionDir(sessionID)
	if err != nil {
		return "", nil, err
	}
	s.lifecycle.RLock()
	if s.closed {
		s.lifecycle.RUnlock()
		return "", nil, ErrFileClosed
	}
	unlockSession := s.lockSession(sessionID)
	return directory, func() {
		unlockSession()
		s.lifecycle.RUnlock()
	}, nil
}

func (s *File) lockSession(sessionID string) func() {
	s.locksMu.Lock()
	lock := s.sessionLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.sessionLocks[sessionID] = lock
	}
	s.locksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *File) beginArtifact(sessionID string) (string, func(), error) {
	directory, err := s.sessionDir(sessionID)
	if err != nil {
		return "", nil, err
	}
	// Count the call as in-flight before it queues for the artifact lock. Close
	// therefore waits for operations that have already entered the Store, while
	// later calls block behind the lifecycle writer and observe ErrFileClosed.
	// No event lock is held while waiting for artifact serialization.
	s.lifecycle.RLock()
	if s.closed {
		s.lifecycle.RUnlock()
		return "", nil, ErrFileClosed
	}
	s.artifactsMu.Lock()
	lock := s.artifactLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.artifactLocks[sessionID] = lock
	}
	s.artifactsMu.Unlock()
	lock.Lock()
	return directory, func() {
		lock.Unlock()
		s.lifecycle.RUnlock()
	}, nil
}

func (s *File) confirmArtifactSession(sessionID string, directory string) error {
	unlockSession := s.lockSession(sessionID)
	defer unlockSession()
	if err := s.ensureSessionDurability(sessionID, directory); err != nil {
		return err
	}
	if err := s.rejectDurabilityUncertainty(sessionID); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(directory, "events.jsonl")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rinruntime.ErrNotFound
		}
		return err
	}
	return nil
}

func (s *File) beginRoot() (func(), error) {
	s.lifecycle.RLock()
	if s.closed {
		s.lifecycle.RUnlock()
		return nil, ErrFileClosed
	}
	return s.lifecycle.RUnlock, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func (s *File) sessionDurabilityIsConfirmed(sessionID string) bool {
	s.durabilityMu.Lock()
	defer s.durabilityMu.Unlock()
	_, confirmed := s.durabilityConfirmed[sessionID]
	return confirmed
}

func (s *File) markSessionDurabilityConfirmed(sessionID string) {
	s.durabilityMu.Lock()
	defer s.durabilityMu.Unlock()
	if s.durabilityConfirmed == nil {
		s.durabilityConfirmed = make(map[string]struct{})
	}
	s.durabilityConfirmed[sessionID] = struct{}{}
}

// ensureSessionDurability establishes a fresh post-Open durability fence before
// this File instance trusts an existing Session. This replaces any in-memory
// uncertainty state lost across Close/reopen with an authoritative sync of the
// bytes and namespace entries currently visible on disk.
func (s *File) ensureSessionDurability(sessionID string, directory string) error {
	if s.sessionDurabilityIsConfirmed(sessionID) {
		return nil
	}
	eventPath := filepath.Join(directory, "events.jsonl")
	if err := s.syncEventFile(eventPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rinruntime.ErrNotFound
		}
		return fmt.Errorf("sync session event log before first use: %w", err)
	}
	if err := s.syncDir(directory); err != nil {
		return fmt.Errorf("sync session directory before first use: %w", err)
	}
	if err := s.syncDir(filepath.Dir(directory)); err != nil {
		return fmt.Errorf("sync sessions directory before first use: %w", err)
	}
	s.markSessionDurabilityConfirmed(sessionID)
	return nil
}

// makeDirectoryTreeSynced creates each missing component separately and syncs
// its parent before proceeding. The nearest existing boundary is fenced first:
// it may be an intermediate directory created by a previous attempt whose
// parent sync failed. Avoid walking and opening unrelated ancestors up to /,
// because a valid path may contain execute-only directories.
func makeDirectoryTreeSynced(path string, mode os.FileMode) error {
	return makeDirectoryTreeSyncedWith(path, mode, syncDirectory)
}

func makeDirectoryTreeSyncedWith(
	path string,
	mode os.FileMode,
	syncDir func(string) error,
) error {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	boundary := ""
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", current)
			}
			boundary = current
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("no existing parent for %s", path)
		}
	}
	if err := syncDir(filepath.Dir(boundary)); err != nil {
		return err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		directory := missing[index]
		if err := os.Mkdir(directory, mode); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr := os.Stat(directory)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", directory)
			}
		}
		if err := syncDir(filepath.Dir(directory)); err != nil {
			return err
		}
	}
	return nil
}

func (s *File) cleanupTemporaryFiles() error {
	sessions := filepath.Join(s.root, "sessions")
	entries, err := os.ReadDir(sessions)
	if err != nil {
		return fmt.Errorf("scan temporary session files: %w", err)
	}
	removed := false
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".session-") &&
			strings.HasSuffix(entry.Name(), ".tmp") {
			if err := os.RemoveAll(filepath.Join(sessions, entry.Name())); err != nil {
				return fmt.Errorf("remove abandoned session temporary directory: %w", err)
			}
			removed = true
			continue
		}
		if !entry.IsDir() || !safeID.MatchString(entry.Name()) {
			continue
		}
		directory := filepath.Join(sessions, entry.Name())
		children, readErr := os.ReadDir(directory)
		if readErr != nil {
			return fmt.Errorf("scan session %s: %w", entry.Name(), readErr)
		}
		sessionRemoved := false
		for _, child := range children {
			name := child.Name()
			if child.IsDir() {
				continue
			}
			if (strings.HasPrefix(name, ".snapshot-") ||
				strings.HasPrefix(name, ".checkpoint-") ||
				strings.HasPrefix(name, ".events.idx-")) &&
				strings.HasSuffix(name, ".tmp") {
				if err := os.Remove(filepath.Join(directory, name)); err != nil &&
					!errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove abandoned session temporary file: %w", err)
				}
				sessionRemoved = true
			}
		}
		if sessionRemoved {
			if err := syncDirectory(directory); err != nil {
				return fmt.Errorf("sync cleaned session directory: %w", err)
			}
		}
	}
	if removed {
		if err := syncDirectory(sessions); err != nil {
			return fmt.Errorf("sync cleaned sessions directory: %w", err)
		}
	}
	return nil
}

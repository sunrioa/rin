package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

// ReadOnlyFile exposes the authoritative event log and in-memory range indexes.
// Base Store writes return ErrReadOnly, and optional checkpoint, lifecycle, and
// Transfer write capabilities are absent. It still holds the data-directory
// lease so all reads observe an offline, stable directory.
type ReadOnlyFile struct {
	file *File
}

var (
	_ rinruntime.Store      = (*ReadOnlyFile)(nil)
	_ rinruntime.RangeStore = (*ReadOnlyFile)(nil)
)

// OpenFileReadOnly opens an existing File Store without changing its durable
// files. The caller must call Close to release the exclusive directory lease.
func OpenFileReadOnly(root string) (*ReadOnlyFile, error) {
	if root == "" {
		return nil, errors.New("data directory is required")
	}
	if err := checkDataDirectoryLockSupport(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absolute, err = validateRealDataDirectory(absolute)
	if err != nil {
		return nil, err
	}
	sessions := filepath.Join(absolute, "sessions")
	if err := requireRealDirectory(sessions, "sessions"); err != nil {
		return nil, err
	}
	if err := requireRealDirectory(
		filepath.Join(absolute, "tombstones"),
		"tombstones",
	); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(absolute, ".rin.lock")
	if err := validateRealLockFile(lockPath, true); err != nil {
		return nil, err
	}
	lockFile, err := acquireExistingDataDirectoryLock(lockPath)
	if err != nil {
		return nil, err
	}
	file := &File{
		root: absolute, lockFile: lockFile, readOnly: true,
		sessionLocks:        make(map[string]*sync.Mutex),
		indexes:             make(map[string]*eventIndex),
		artifactLocks:       make(map[string]*sync.Mutex),
		uncertainAppends:    make(map[string]uncertainFileAppend),
		durabilityConfirmed: make(map[string]struct{}),
	}
	return &ReadOnlyFile{file: file}, nil
}

func (store *ReadOnlyFile) Close() error {
	return store.file.Close()
}

func (store *ReadOnlyFile) Create(
	string,
	protocol.EventRecord,
) error {
	return ErrReadOnly
}

func (store *ReadOnlyFile) Append(
	string,
	protocol.EventRecord,
) error {
	return ErrReadOnly
}

func (store *ReadOnlyFile) Load(
	sessionID string,
) ([]protocol.EventRecord, error) {
	return store.file.Load(sessionID)
}

func (store *ReadOnlyFile) ListSessions() ([]string, error) {
	sessionIDs, err := store.file.ListSessions()
	if err != nil {
		return nil, err
	}
	retained := sessionIDs[:0]
	for _, sessionID := range sessionIDs {
		tombstone := filepath.Join(
			store.file.root,
			"tombstones",
			sessionID+tombstoneSuffix,
		)
		if _, statErr := os.Stat(tombstone); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf(
				"inspect Session %s tombstone: %w",
				sessionID,
				statErr,
			)
		}
		retained = append(retained, sessionID)
	}
	return retained, nil
}

func (store *ReadOnlyFile) SaveSnapshot(
	string,
	protocol.Snapshot,
) error {
	return ErrReadOnly
}

func (store *ReadOnlyFile) Head(
	sessionID string,
) (rinruntime.EventAnchor, error) {
	return store.file.Head(sessionID)
}

func (store *ReadOnlyFile) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rinruntime.EventPage, error) {
	return store.file.LoadRange(
		sessionID,
		afterRevision,
		throughRevision,
		limit,
	)
}

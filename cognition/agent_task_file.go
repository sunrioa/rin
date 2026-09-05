package cognition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sunrioa/rin/internal/privatefile"
)

const maxTaskSnapshotBytes = 64 << 20

var (
	ErrTaskStoreLocked      = errors.New("cognition task store is already locked")
	ErrTaskStoreClosed      = errors.New("cognition task store is closed")
	ErrTaskStorePersistence = errors.New("cognition task store persistence failed")
)

// FileTaskStore is the durable single-writer TaskStore used by a Rin daemon.
// Every successful mutation is atomically persisted before it is returned.
type FileTaskStore struct {
	mu           sync.Mutex
	path         string
	maxTasks     uint32
	lockFile     *os.File
	local        *LocalTaskStore
	closed       bool
	writeBlocked error
}

func OpenFileTaskStore(path string, maxTasks uint32) (*FileTaskStore, error) {
	if path == "" {
		return nil, errors.New("task store path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := prepareProviderStorePath(
		absolute, ErrTaskStorePersistence, "task store",
	); err != nil {
		return nil, err
	}
	lockFile, err := acquireProviderStoreLock(
		absolute+".lock", ErrTaskStoreLocked, ErrTaskStorePersistence, "task store",
	)
	if err != nil {
		return nil, err
	}
	store := &FileTaskStore{
		path: absolute, maxTasks: maxTasks, lockFile: lockFile,
	}
	if filepath.Ext(absolute) == ".json" {
		if _, err := os.Lstat(strings.TrimSuffix(absolute, ".json") + ".db"); !errors.Is(err, os.ErrNotExist) {
			_ = releaseProviderStoreLock(lockFile)
			return nil, fmt.Errorf("%w: task database exists; use OpenSQLiteTaskStore", ErrTaskStorePersistence)
		}
	}
	var snapshot TaskSnapshot
	if err := privatefile.ReadJSON(absolute, maxTaskSnapshotBytes, &snapshot); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = releaseProviderStoreLock(lockFile)
			return nil, fmt.Errorf("%w: load snapshot: %v", ErrTaskStorePersistence, err)
		}
		store.local, err = NewLocalTaskStore(maxTasks)
		if err == nil {
			snapshot, err = store.local.Snapshot(context.Background())
		}
		if err == nil {
			err = privatefile.WriteJSONBounded(absolute, snapshot, maxTaskSnapshotBytes)
		}
	} else {
		store.local, err = RestoreLocalTaskStore(maxTasks, snapshot)
	}
	if err != nil {
		_ = releaseProviderStoreLock(lockFile)
		return nil, fmt.Errorf("%w: initialize snapshot: %v", ErrTaskStorePersistence, err)
	}
	return store, nil
}

func (store *FileTaskStore) Create(
	ctx context.Context,
	task TaskSession,
) (TaskSession, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSession{}, err
	}
	created, err := store.local.Create(ctx, task)
	if err != nil {
		return TaskSession{}, err
	}
	if err := store.persistLocked(); err != nil {
		return TaskSession{}, err
	}
	return created, nil
}

func (store *FileTaskStore) Load(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSession{}, err
	}
	return store.local.Load(ctx, taskID)
}

func (store *FileTaskStore) CompareAndSwap(
	ctx context.Context,
	expectedRevision uint64,
	task TaskSession,
) (TaskSession, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSession{}, err
	}
	updated, err := store.local.CompareAndSwap(ctx, expectedRevision, task)
	if err != nil {
		return TaskSession{}, err
	}
	if err := store.persistLocked(); err != nil {
		return TaskSession{}, err
	}
	return updated, nil
}

func (store *FileTaskStore) Snapshot(ctx context.Context) (TaskSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSnapshot{}, err
	}
	return store.local.Snapshot(ctx)
}

func (store *FileTaskStore) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	err := releaseProviderStoreLock(store.lockFile)
	store.lockFile = nil
	return err
}

func (store *FileTaskStore) persistLocked() error {
	// Once the in-memory CAS succeeds, caller cancellation cannot roll back the
	// mutation. Finish the atomic disk commit before returning.
	snapshot, err := store.local.Snapshot(context.Background())
	if err != nil {
		return err
	}
	if err := privatefile.WriteJSONBounded(
		store.path, snapshot, maxTaskSnapshotBytes,
	); err != nil {
		store.writeBlocked = fmt.Errorf("%w: %v", ErrTaskStorePersistence, err)
		return store.writeBlocked
	}
	return nil
}

func (store *FileTaskStore) ready() error {
	if store.closed {
		return ErrTaskStoreClosed
	}
	return store.writeBlocked
}

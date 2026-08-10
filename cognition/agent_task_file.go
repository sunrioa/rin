package cognition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	if err := prepareTaskStorePath(absolute); err != nil {
		return nil, err
	}
	lockFile, err := acquireTaskStoreLock(absolute + ".lock")
	if err != nil {
		return nil, err
	}
	store := &FileTaskStore{
		path: absolute, maxTasks: maxTasks, lockFile: lockFile,
	}
	var snapshot TaskSnapshot
	if err := privatefile.ReadJSON(absolute, maxTaskSnapshotBytes, &snapshot); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = releaseTaskStoreLock(lockFile)
			return nil, fmt.Errorf("%w: load snapshot: %v", ErrTaskStorePersistence, err)
		}
		store.local, err = NewLocalTaskStore(maxTasks)
		if err == nil {
			snapshot, err = store.local.Snapshot(context.Background())
		}
		if err == nil {
			err = privatefile.WriteJSON(absolute, snapshot)
		}
	} else {
		store.local, err = RestoreLocalTaskStore(maxTasks, snapshot)
	}
	if err != nil {
		_ = releaseTaskStoreLock(lockFile)
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
	if err := store.persistLocked(ctx); err != nil {
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
	if err := store.persistLocked(ctx); err != nil {
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
	err := releaseTaskStoreLock(store.lockFile)
	store.lockFile = nil
	return err
}

func (store *FileTaskStore) persistLocked(ctx context.Context) error {
	snapshot, err := store.local.Snapshot(ctx)
	if err != nil {
		return err
	}
	if err := privatefile.WriteJSON(store.path, snapshot); err != nil {
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

func prepareTaskStorePath(path string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("%w: create directory: %v", ErrTaskStorePersistence, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("%w: protect directory: %v", ErrTaskStorePersistence, err)
		}
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("task store parent must be a real directory")
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect snapshot: %v", ErrTaskStorePersistence, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("task store snapshot must be a real regular file")
	}
	return nil
}

func openTaskStoreLockFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: lock path is not a real file", ErrTaskStorePersistence)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: inspect lock: %v", ErrTaskStorePersistence, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: open lock: %v", ErrTaskStorePersistence, err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: lock path is not a regular file", ErrTaskStorePersistence)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: protect lock: %v", ErrTaskStorePersistence, err)
	}
	return file, nil
}

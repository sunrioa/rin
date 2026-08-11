package cognition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sunrioa/rin/internal/privatefile"
)

const maxMemorySnapshotBytes = 128 << 20

var (
	ErrMemoryStoreLocked      = errors.New("cognition memory store is already locked")
	ErrMemoryStoreClosed      = errors.New("cognition memory store is closed")
	ErrMemoryStorePersistence = errors.New("cognition memory store persistence failed")
)

// FileMemoryProvider is the durable single-writer MemoryProvider used by a
// Rin daemon. A mutating call is successful only after its snapshot is safely
// replaced on disk.
type FileMemoryProvider struct {
	mu           sync.Mutex
	path         string
	lockFile     *os.File
	local        *LocalMemoryProvider
	closed       bool
	writeBlocked error
}

func OpenFileMemoryProvider(
	path string,
	config LocalMemoryConfig,
) (*FileMemoryProvider, error) {
	if path == "" {
		return nil, errors.New("memory store path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := prepareProviderStorePath(
		absolute, ErrMemoryStorePersistence, "memory store",
	); err != nil {
		return nil, err
	}
	lockFile, err := acquireProviderStoreLock(
		absolute+".lock", ErrMemoryStoreLocked, ErrMemoryStorePersistence, "memory store",
	)
	if err != nil {
		return nil, err
	}
	store := &FileMemoryProvider{path: absolute, lockFile: lockFile}
	var snapshot MemorySnapshot
	if err := privatefile.ReadJSON(absolute, maxMemorySnapshotBytes, &snapshot); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = releaseProviderStoreLock(lockFile)
			return nil, fmt.Errorf("%w: load snapshot: %v", ErrMemoryStorePersistence, err)
		}
		store.local, err = NewLocalMemoryProvider(config)
		if err == nil {
			snapshot, err = store.local.Snapshot(context.Background())
		}
		if err == nil {
			err = privatefile.WriteJSONBounded(absolute, snapshot, maxMemorySnapshotBytes)
		}
	} else {
		store.local, err = RestoreLocalMemoryProvider(config, snapshot)
	}
	if err != nil {
		_ = releaseProviderStoreLock(lockFile)
		return nil, fmt.Errorf("%w: initialize snapshot: %v", ErrMemoryStorePersistence, err)
	}
	return store, nil
}

func (store *FileMemoryProvider) Append(
	ctx context.Context,
	record MemoryRecord,
) (MemoryRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return MemoryRecord{}, err
	}
	before := store.local.revisionValue()
	appended, err := store.local.Append(ctx, record)
	if err != nil {
		return MemoryRecord{}, err
	}
	if err := store.persistIfChangedLocked(before); err != nil {
		return MemoryRecord{}, err
	}
	return appended, nil
}

func (store *FileMemoryProvider) Retrieve(
	ctx context.Context,
	query MemoryQuery,
) ([]MemoryMatch, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return nil, err
	}
	before := store.local.revisionValue()
	matches, err := store.local.Retrieve(ctx, query)
	if err != nil {
		return nil, err
	}
	if err := store.persistIfChangedLocked(before); err != nil {
		return nil, err
	}
	return matches, nil
}

func (store *FileMemoryProvider) Consolidate(
	ctx context.Context,
	request MemoryConsolidation,
) (MemoryRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return MemoryRecord{}, err
	}
	before := store.local.revisionValue()
	consolidated, err := store.local.Consolidate(ctx, request)
	if err != nil {
		return MemoryRecord{}, err
	}
	if err := store.persistIfChangedLocked(before); err != nil {
		return MemoryRecord{}, err
	}
	return consolidated, nil
}

func (store *FileMemoryProvider) Forget(
	ctx context.Context,
	request MemoryForgetRequest,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return err
	}
	before := store.local.revisionValue()
	if err := store.local.Forget(ctx, request); err != nil {
		return err
	}
	return store.persistIfChangedLocked(before)
}

func (store *FileMemoryProvider) Snapshot(ctx context.Context) (MemorySnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return MemorySnapshot{}, err
	}
	return store.local.Snapshot(ctx)
}

func (store *FileMemoryProvider) Health(ctx context.Context) ProviderHealth {
	if ctx == nil || ctx.Err() != nil {
		return ProviderHealth{Code: "context_unavailable"}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return ProviderHealth{Degraded: true, Code: "memory_store_unavailable"}
	}
	return store.local.Health(ctx)
}

func (store *FileMemoryProvider) Close() error {
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

func (store *FileMemoryProvider) persistIfChangedLocked(
	before uint64,
) error {
	if store.local.revisionValue() == before {
		return nil
	}
	// Retrieval and mutation may already have changed provider-owned state.
	// Caller cancellation cannot make that state safely disappear, so finish
	// the atomic commit before returning.
	snapshot, err := store.local.Snapshot(context.Background())
	if err != nil {
		return err
	}
	if err := privatefile.WriteJSONBounded(
		store.path, snapshot, maxMemorySnapshotBytes,
	); err != nil {
		store.writeBlocked = fmt.Errorf("%w: %v", ErrMemoryStorePersistence, err)
		return store.writeBlocked
	}
	return nil
}

func (store *FileMemoryProvider) ready() error {
	if store.closed {
		return ErrMemoryStoreClosed
	}
	return store.writeBlocked
}

var _ MemoryProvider = (*FileMemoryProvider)(nil)

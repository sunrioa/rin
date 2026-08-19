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

const maxPersonaSnapshotBytes = 4 << 20

var (
	ErrPersonaStoreLocked      = errors.New("persona store is already locked")
	ErrPersonaStoreClosed      = errors.New("persona store is closed")
	ErrPersonaStorePersistence = errors.New("persona store persistence failed")
)

// FilePersonaStore is the durable single-writer PersonaStore used by a Rin
// daemon. The seed is used only when the file does not exist; later Console
// edits remain authoritative across restarts.
type FilePersonaStore struct {
	mu       sync.Mutex
	path     string
	lockFile *os.File
	local    *LocalPersonaProvider
	closed   bool
}

func OpenFilePersonaStore(
	path string,
	seed PersonaSnapshot,
) (*FilePersonaStore, error) {
	if path == "" {
		return nil, errors.New("persona store path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := prepareProviderStorePath(
		absolute, ErrPersonaStorePersistence, "persona store",
	); err != nil {
		return nil, err
	}
	lockFile, err := acquireProviderStoreLock(
		absolute+".lock", ErrPersonaStoreLocked, ErrPersonaStorePersistence, "persona store",
	)
	if err != nil {
		return nil, err
	}
	store := &FilePersonaStore{path: absolute, lockFile: lockFile}
	var snapshot PersonaSnapshot
	if err := privatefile.ReadJSON(absolute, maxPersonaSnapshotBytes, &snapshot); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = releaseProviderStoreLock(lockFile)
			return nil, fmt.Errorf("%w: load snapshot: %v", ErrPersonaStorePersistence, err)
		}
		if len(seed.Profiles) == 0 && len(seed.Bindings) == 0 {
			seed = DefaultPersonaSnapshot()
		} else {
			seed = withSeedDefaultPersonaBinding(seed)
		}
		if seed.Revision == 0 {
			seed.Revision = 1
		}
		store.local, err = RestoreLocalPersonaProvider(seed)
		if err == nil {
			snapshot, err = store.local.Snapshot(context.Background())
		}
		if err == nil {
			err = privatefile.WriteJSONBounded(absolute, snapshot, maxPersonaSnapshotBytes)
		}
	} else {
		store.local, err = RestoreLocalPersonaProvider(snapshot)
	}
	if err == nil {
		var current PersonaSnapshot
		current, err = store.local.Snapshot(context.Background())
		if err == nil {
			err = requireDefaultPersonaBinding(current)
		}
	}
	if err != nil {
		_ = releaseProviderStoreLock(lockFile)
		return nil, fmt.Errorf("%w: initialize snapshot: %v", ErrPersonaStorePersistence, err)
	}
	return store, nil
}

func withSeedDefaultPersonaBinding(snapshot PersonaSnapshot) PersonaSnapshot {
	for _, binding := range snapshot.Bindings {
		if binding.ActorID == "" && binding.ControllerID == "" {
			return snapshot
		}
	}
	if len(snapshot.Profiles) == 0 {
		return snapshot
	}
	profile := snapshot.Profiles[0]
	snapshot.Bindings = append(snapshot.Bindings, PersonaBinding{
		PersonaID: profile.PersonaID,
		Version:   profile.Version,
	})
	return snapshot
}

func DefaultPersonaSnapshot() PersonaSnapshot {
	return PersonaSnapshot{
		Revision: 1,
		Profiles: []PersonaProfile{{
			PersonaID: "persona.rin-default",
			Version:   "v1",
			Identity:  "A persistent game companion that observes before acting and stays honest about outcomes.",
			Traits:    []string{"helpful", "observant", "adaptable"},
			Values:    []string{"respect player intent", "learn from authoritative outcomes"},
			Voice:     "Concise, natural, and grounded in the current game context.",
			PresentationRules: []string{
				"Do not claim an action succeeded without an authoritative outcome.",
			},
		}},
		Bindings: []PersonaBinding{{
			PersonaID: "persona.rin-default", Version: "v1",
		}},
	}
}

func (store *FilePersonaStore) Load(
	ctx context.Context,
	request PersonaRequest,
) (PersonaProfile, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PersonaProfile{}, err
	}
	return store.local.Load(ctx, request)
}

func (store *FilePersonaStore) Snapshot(ctx context.Context) (PersonaSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PersonaSnapshot{}, err
	}
	return store.local.Snapshot(ctx)
}

func (store *FilePersonaStore) CompareAndSwap(
	ctx context.Context,
	snapshot PersonaSnapshot,
) (PersonaSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PersonaSnapshot{}, err
	}
	current, err := store.local.Snapshot(ctx)
	if err != nil {
		return PersonaSnapshot{}, err
	}
	if snapshot.Revision != current.Revision {
		return PersonaSnapshot{}, ErrPersonaConflict
	}
	if err := requireDefaultPersonaBinding(snapshot); err != nil {
		return PersonaSnapshot{}, err
	}
	candidate := snapshot
	candidate.Revision++
	replacement, err := RestoreLocalPersonaProvider(candidate)
	if err != nil {
		return PersonaSnapshot{}, err
	}
	updated, err := replacement.Snapshot(context.Background())
	if err != nil {
		return PersonaSnapshot{}, err
	}
	if err := store.persistSnapshotLocked(updated); err != nil {
		return PersonaSnapshot{}, err
	}
	store.local = replacement
	return updated, nil
}

func (store *FilePersonaStore) Health(ctx context.Context) ProviderHealth {
	store.mu.Lock()
	defer store.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || store.ready() != nil {
		return ProviderHealth{Code: "persona_store_unavailable"}
	}
	return ProviderHealth{Available: true}
}

func (store *FilePersonaStore) Close() error {
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

func (store *FilePersonaStore) persistSnapshotLocked(snapshot PersonaSnapshot) error {
	if err := privatefile.WriteJSONBounded(
		store.path, snapshot, maxPersonaSnapshotBytes,
	); err != nil {
		return fmt.Errorf("%w: %v", ErrPersonaStorePersistence, err)
	}
	return nil
}

func (store *FilePersonaStore) ready() error {
	if store.closed {
		return ErrPersonaStoreClosed
	}
	return nil
}

func requireDefaultPersonaBinding(snapshot PersonaSnapshot) error {
	defaults := 0
	for _, binding := range snapshot.Bindings {
		if binding.ActorID == "" && binding.ControllerID == "" {
			defaults++
		}
	}
	if defaults != 1 {
		return errors.New("persona snapshot must contain exactly one default binding")
	}
	return nil
}

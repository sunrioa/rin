package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/sunrioa/rin/internal/privatefile"
	"github.com/sunrioa/rin/managementapi"
	"github.com/sunrioa/rin/policy"
)

type policyConfigStore struct {
	mu         sync.Mutex
	path       string
	engine     *policy.Engine
	configured bool
}

func managedPolicyConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "policy", "policy.json")
}

func openPolicyConfigStore(
	dataDir, runtimePath string,
	engine *policy.Engine,
) (*policyConfigStore, error) {
	if engine == nil {
		return nil, fmt.Errorf("gameplay policy engine is required")
	}
	path := managedPolicyConfigPath(dataDir)
	configured := false
	if runtimePath != "" {
		path = runtimePath
		configured = true
	}
	return &policyConfigStore{path: path, engine: engine, configured: configured}, nil
}

func (store *policyConfigStore) PolicyConfig(
	ctx context.Context,
) (managementapi.PolicyConfigSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return managementapi.PolicyConfigSnapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshotLocked(), nil
}

func (store *policyConfigStore) snapshotLocked() managementapi.PolicyConfigSnapshot {
	return managementapi.PolicyConfigSnapshot{
		Configured: store.configured,
		Config:     store.engine.Config(),
	}
}

func (store *policyConfigStore) SavePolicyConfig(
	ctx context.Context,
	request managementapi.PolicyConfigSaveRequest,
) (managementapi.PolicyConfigSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return managementapi.PolicyConfigSnapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.engine.Config()
	if request.ExpectedRevision != current.Revision {
		return managementapi.PolicyConfigSnapshot{}, fmt.Errorf(
			"%w: current revision is %d", managementapi.ErrPolicyConfigConflict, current.Revision,
		)
	}
	request.Config.Revision = current.Revision + 1
	validated, err := policy.New(request.Config)
	if err != nil {
		return managementapi.PolicyConfigSnapshot{}, fmt.Errorf(
			"%w: %v", managementapi.ErrInvalidPolicyConfig, err,
		)
	}
	config := validated.Config()
	if err := privatefile.WriteJSON(store.path, config); err != nil {
		return managementapi.PolicyConfigSnapshot{}, fmt.Errorf("save gameplay policy: %w", err)
	}
	if err := store.engine.Update(config); err != nil {
		return managementapi.PolicyConfigSnapshot{}, fmt.Errorf("activate gameplay policy: %w", err)
	}
	store.configured = true
	return store.snapshotLocked(), nil
}

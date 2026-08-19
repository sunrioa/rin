package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/sunrioa/rin/managementapi"
	"github.com/sunrioa/rin/policy"
)

func TestPolicyConfigStorePersistsAndActivatesNewRevision(t *testing.T) {
	engine, err := loadPolicyEngine("")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	store, err := openPolicyConfigStore(dataDir, "", engine)
	if err != nil {
		t.Fatal(err)
	}
	config := engine.Config()
	config.Profile = policy.ProfileOpen
	snapshot, err := store.SavePolicyConfig(context.Background(), managementapi.PolicyConfigSaveRequest{
		ExpectedRevision: config.Revision,
		Config:           config,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Configured || snapshot.Config.Revision != 2 || snapshot.Config.Profile != policy.ProfileOpen {
		t.Fatalf("saved policy snapshot = %#v", snapshot)
	}
	if active := engine.Config(); active.Revision != 2 || active.Profile != policy.ProfileOpen {
		t.Fatalf("active policy = %#v", active)
	}
	path := managedPolicyConfigPath(dataDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("policy mode = %o, want 600", info.Mode().Perm())
	}
	reopened, err := loadPolicyEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded := reopened.Config(); loaded.Revision != 2 || loaded.Profile != policy.ProfileOpen {
		t.Fatalf("reopened policy = %#v", loaded)
	}
}

func TestPolicyConfigStoreRejectsStaleAndInvalidEdits(t *testing.T) {
	engine, err := loadPolicyEngine("")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openPolicyConfigStore(t.TempDir(), "", engine)
	if err != nil {
		t.Fatal(err)
	}
	config := engine.Config()
	_, err = store.SavePolicyConfig(context.Background(), managementapi.PolicyConfigSaveRequest{
		ExpectedRevision: config.Revision + 1,
		Config:           config,
	})
	if !errors.Is(err, managementapi.ErrPolicyConfigConflict) {
		t.Fatalf("stale save result = %v", err)
	}
	config.Profile = "unknown"
	_, err = store.SavePolicyConfig(context.Background(), managementapi.PolicyConfigSaveRequest{
		ExpectedRevision: config.Revision,
		Config:           config,
	})
	if !errors.Is(err, managementapi.ErrInvalidPolicyConfig) {
		t.Fatalf("invalid save result = %v", err)
	}
	if engine.Config().Revision != 1 {
		t.Fatal("invalid policy changed the active revision")
	}
}

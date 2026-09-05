package signalbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
)

func TestSQLiteSignalNeverAcknowledgesUncommittedPublication(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "signals.db")
	now := time.UnixMilli(1000)
	config := StoreConfig{Now: func() time.Time { return now }, DefaultSettings: Settings{Enabled: true, MaxPending: 8}}
	store, err := OpenSQLiteStore(path, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.durable.db.Exec(`CREATE TRIGGER reject_signal BEFORE UPDATE ON signal_meta BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	signal := Signal{SignalID: "signal.test", HostID: "host.one", WorldID: "world.one", ActorID: "actor.one", Kind: "game.event", Summary: "An event occurred.", Epoch: host.Epoch{SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1}, ObservationSequence: 1, ExpiresAtUnixMillis: now.Add(time.Hour).UnixMilli()}
	result, err := store.Publish(signal)
	if !errors.Is(err, ErrPersistence) || result.Accepted {
		t.Fatalf("uncommitted publication acknowledged: %#v %v", result, err)
	}
	if _, err := store.WaitPending(context.Background(), 0); !errors.Is(err, ErrPersistence) {
		t.Fatalf("failed write exposed cached signal: %v", err)
	}
	if _, err := store.durable.db.Exec(`DROP TRIGGER reject_signal`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteStore(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err := store.WaitPending(context.Background(), 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("rolled back signal restored: %#v %v", pending, err)
	}
	if result, err := store.Publish(signal); err != nil || !result.Accepted || result.Cursor != 1 {
		t.Fatalf("retry after reopen: %#v %v", result, err)
	}
}

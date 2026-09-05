package signalbox_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/signalbox"
)

func TestSQLiteInboxRestoresPendingRetryACKSettingsAndCursor(t *testing.T) {
	now := time.UnixMilli(1000)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "signals.db")
	config := signalbox.StoreConfig{Now: func() time.Time { return now }}
	store, err := signalbox.OpenSQLiteStore(path, config)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := signalbox.OpenSQLiteStore(path, config); err == nil {
		other.Close()
		t.Fatal("second writer opened")
	}
	target := signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"}
	if _, err := store.Configure(target, signalbox.Settings{Enabled: true, MaxPending: 8}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"signal.pending", "signal.retry", "signal.acked", "signal.expired"} {
		expires := now.Add(time.Hour)
		if id == "signal.expired" {
			expires = now.Add(time.Second)
		}
		if result, err := store.Publish(testSignal(id, "game.event", expires)); err != nil || !result.Accepted {
			t.Fatalf("publish: %#v %v", result, err)
		}
	}
	pending, err := store.WaitPending(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDelivery(pending[1], "retry", "task-running", "task.one"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDelivery(pending[2], "attached", "", "task.one"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	store, err = signalbox.OpenSQLiteStore(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err = store.WaitPending(context.Background(), 0)
	if err != nil || len(pending) != 2 || pending[0].SignalID != "signal.pending" || pending[1].Delivery.Attempts != 1 {
		t.Fatalf("restored pending: %#v %v", pending, err)
	}
	duplicate, err := store.Publish(testSignal("signal.pending", "game.event", time.UnixMilli(1000).Add(time.Hour)))
	if err != nil || duplicate.Reason != "duplicate" {
		t.Fatalf("dedup: %#v %v", duplicate, err)
	}
	result, err := store.Publish(testSignal("signal.new", "game.event", now.Add(time.Hour)))
	if err != nil || !result.Accepted || result.Cursor != 5 {
		t.Fatalf("cursor/settings: %#v %v", result, err)
	}
	page, err := store.List(signalbox.ListInput{Target: target})
	if err != nil || len(page.Signals) != 4 || page.Signals[2].Delivery.Status != "attached" {
		t.Fatalf("ACK state: %#v %v", page, err)
	}
}

func TestSQLiteExplicitDefaultSettingsRemainExplicitAfterRestart(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "signals.db")
	target := signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"}
	store, err := signalbox.OpenSQLiteStore(path, signalbox.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.Settings(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Configure(target, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	settings.Enabled = true
	store, err = signalbox.OpenSQLiteStore(path, signalbox.StoreConfig{DefaultSettings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.Settings(target)
	if err != nil || restored.Enabled {
		t.Fatalf("explicit disabled setting replaced by new default: %#v %v", restored, err)
	}
}

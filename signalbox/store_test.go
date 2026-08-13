package signalbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/signalbox"
)

func TestStoreAppliesSettingsDedupCooldownCapacityAndExpiry(t *testing.T) {
	now := time.UnixMilli(1_000)
	store, err := signalbox.NewStore(signalbox.StoreConfig{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	target := signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"}
	if _, err := store.Configure(target, signalbox.Settings{
		Enabled: true, CooldownMillis: 100, MaxPending: 2,
	}); err != nil {
		t.Fatal(err)
	}
	first := testSignal("signal.one", "game.player.hurt", now.Add(time.Second))
	accepted, err := store.Publish(first)
	if err != nil || !accepted.Accepted || accepted.Cursor != 1 {
		t.Fatalf("first = %#v, %v", accepted, err)
	}
	duplicate, err := store.Publish(first)
	if err != nil || duplicate.Accepted || duplicate.Reason != "duplicate" {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
	second := testSignal("signal.two", "game.player.hurt", now.Add(time.Second))
	cooled, err := store.Publish(second)
	if err != nil || cooled.Accepted || cooled.Reason != "cooldown" {
		t.Fatalf("cooldown = %#v, %v", cooled, err)
	}
	now = now.Add(101 * time.Millisecond)
	if accepted, err = store.Publish(second); err != nil || !accepted.Accepted {
		t.Fatalf("second = %#v, %v", accepted, err)
	}
	third := testSignal("signal.three", "game.player.waiting", now.Add(time.Second))
	full, err := store.Publish(third)
	if err != nil || full.Accepted || full.Reason != "capacity" {
		t.Fatalf("capacity = %#v, %v", full, err)
	}
	page, err := store.List(signalbox.ListInput{Target: target, Limit: 1})
	if err != nil || len(page.Signals) != 1 || !page.More || page.NextCursor != 1 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	now = now.Add(time.Second)
	page, err = store.List(signalbox.ListInput{Target: target})
	if err != nil || len(page.Signals) != 0 || page.NextCursor != 0 {
		t.Fatalf("expired page = %#v, %v", page, err)
	}
}

func TestStoreWaitsByActorCursorAndInternalSequence(t *testing.T) {
	now := time.UnixMilli(1_000)
	store, _ := signalbox.NewStore(signalbox.StoreConfig{Now: func() time.Time { return now }})
	target := signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"}
	_, _ = store.Configure(target, signalbox.Settings{Enabled: true, MaxPending: 8})
	result := make(chan signalbox.Update, 1)
	errorsFound := make(chan error, 1)
	go func() {
		update, err := store.Wait(context.Background(), signalbox.WaitInput{
			ListInput: signalbox.ListInput{Target: target}, WaitMillis: 1_000,
		})
		if err != nil {
			errorsFound <- err
			return
		}
		result <- update
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := store.Publish(testSignal("signal.one", "game.player.hurt", now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errorsFound:
		t.Fatal(err)
	case update := <-result:
		if !update.Changed || len(update.Page.Signals) != 1 {
			t.Fatalf("wait update = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("actor wait did not wake")
	}
	items, cursor, err := store.WaitAny(context.Background(), 0, 0)
	if err != nil || len(items) != 1 || cursor != 1 {
		t.Fatalf("internal feed = %#v, %d, %v", items, cursor, err)
	}
	if _, err := store.List(signalbox.ListInput{Target: target, AfterCursor: 2}); !errors.Is(err, signalbox.ErrInvalid) {
		t.Fatalf("future cursor error = %v", err)
	}
}

func TestStoreDefaultsToDisabledAndRejectsStaleRuntimeFields(t *testing.T) {
	now := time.UnixMilli(1_000)
	store, _ := signalbox.NewStore(signalbox.StoreConfig{Now: func() time.Time { return now }})
	signal := testSignal("signal.one", "game.player.hurt", now.Add(time.Second))
	ignored, err := store.Publish(signal)
	if err != nil || ignored.Accepted || ignored.Reason != "disabled" {
		t.Fatalf("disabled = %#v, %v", ignored, err)
	}
	signal.Cursor = 1
	if _, err := store.Publish(signal); !errors.Is(err, signalbox.ErrInvalid) {
		t.Fatalf("runtime field error = %v", err)
	}
}

func TestConfigureSameSettingsDoesNotProduceAnUpdate(t *testing.T) {
	now := time.UnixMilli(1_000)
	store, _ := signalbox.NewStore(signalbox.StoreConfig{Now: func() time.Time { return now }})
	target := signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"}
	settings := signalbox.Settings{Enabled: true, MaxPending: 8}
	if _, err := store.Configure(target, settings); err != nil {
		t.Fatal(err)
	}
	done := make(chan signalbox.Update, 1)
	go func() {
		update, _ := store.Wait(context.Background(), signalbox.WaitInput{
			ListInput: signalbox.ListInput{Target: target}, WaitMillis: 50,
		})
		done <- update
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := store.Configure(target, settings); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-done:
		if update.Changed {
			t.Fatalf("unchanged settings produced an inbox update: %#v", update)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("wait did not reach its own timeout")
	}
}

func testSignal(id, kind string, expires time.Time) signalbox.Signal {
	return signalbox.Signal{
		SignalID: id, HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		Kind: kind, Summary: "The player was hurt repeatedly.",
		Epoch: host.Epoch{
			SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
		},
		ObservationSequence: 3, ExpiresAtUnixMillis: expires.UnixMilli(),
	}
}

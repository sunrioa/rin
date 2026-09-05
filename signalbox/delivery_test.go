package signalbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/sunrioa/rin/signalbox"
)

func TestPendingDeliveryRetriesWithoutLosingOtherSignals(t *testing.T) {
	now := time.UnixMilli(1000)
	store, err := signalbox.NewStore(signalbox.StoreConfig{Now: func() time.Time { return now }, DefaultSettings: signalbox.Settings{Enabled: true, MaxPending: 8}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"signal.one", "signal.two"} {
		if result, err := store.Publish(testSignal(id, "game.player.hurt", now.Add(time.Hour))); err != nil || !result.Accepted {
			t.Fatalf("publish = %#v %v", result, err)
		}
	}
	pending, err := store.WaitPending(context.Background(), 0)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %#v %v", pending, err)
	}
	if err := store.RecordDelivery(pending[0], "retry", "task-running", "task.current"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDelivery(pending[1], "attached", "", "task.current"); err != nil {
		t.Fatal(err)
	}
	ready, _ := store.WaitPending(context.Background(), 0)
	if len(ready) != 0 {
		t.Fatalf("retry not delayed: %#v", ready)
	}
	now = now.Add(time.Second)
	ready, _ = store.WaitPending(context.Background(), 0)
	if len(ready) != 1 || ready[0].SignalID != "signal.one" || ready[0].Delivery.Attempts != 1 {
		t.Fatalf("retry lost or successful delivery replayed: %#v", ready)
	}
	if err := store.RecordDelivery(ready[0], "dropped", "external-authority", ""); err != nil {
		t.Fatal(err)
	}
	page, _ := store.List(signalbox.ListInput{Target: signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"}})
	if page.Signals[0].Delivery.Reason != "external-authority" || page.Signals[0].Delivery.Attempts != 2 {
		t.Fatalf("discard reason missing: %#v", page)
	}
	forged := testSignal("signal.forged", "game.player.hurt", now.Add(time.Hour))
	forged.Delivery = signalbox.DeliveryState{Status: "started"}
	if _, err := store.Publish(forged); err == nil {
		t.Fatal("Host forged scheduler delivery")
	}
}

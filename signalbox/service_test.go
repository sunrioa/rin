package signalbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/signalbox"
)

func TestServiceRequiresCurrentHostSnapshotAndActorRead(t *testing.T) {
	now := time.UnixMilli(1_000)
	control := controlplane.New(controlplane.Options{Now: func() time.Time { return now }})
	defer control.Close()
	lease, err := control.RegisterHost(testRegistration())
	if err != nil {
		t.Fatal(err)
	}
	epoch := host.Epoch{
		SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
	}
	if err := control.PublishWorld("host.one", lease.LeaseID, testPublication(epoch, controlplane.DecisionExternal)); err != nil {
		t.Fatal(err)
	}
	client, err := controlplane.NewClientService(control, host.Principal{
		ID: "player.one", GrantedScopes: []string{controlplane.ScopeActorRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := signalbox.NewStore(signalbox.StoreConfig{Now: func() time.Time { return now }})
	service, err := signalbox.NewService(store, control, client)
	if err != nil {
		t.Fatal(err)
	}
	settings := signalbox.HostSettingsInput{
		HostID: "host.one", LeaseID: lease.LeaseID, WorldID: "world.one", ActorID: "actor.one",
		Settings: signalbox.Settings{Enabled: true, MaxPending: 8},
	}
	if _, err := service.ConfigureHost(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	signal := signalbox.Signal{
		SchemaVersion: signalbox.SchemaVersion,
		SignalID:      "signal.one", HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		Kind: "test.player.hurt", Summary: "The player was hurt.", Epoch: epoch,
		ObservationSequence: 2, ExpiresAtUnixMillis: now.Add(time.Second).UnixMilli(),
	}
	stale := signal
	stale.ObservationSequence = 1
	if _, err := service.PublishHost(context.Background(), signalbox.HostPublishInput{
		HostID: "host.one", LeaseID: lease.LeaseID, Signal: stale,
	}); !errors.Is(err, signalbox.ErrInvalid) {
		t.Fatalf("stale signal error = %v", err)
	}
	result, err := service.PublishHost(context.Background(), signalbox.HostPublishInput{
		HostID: "host.one", LeaseID: lease.LeaseID, Signal: signal,
	})
	if err != nil || !result.Accepted {
		t.Fatalf("publish = %#v, %v", result, err)
	}
	page, err := service.List(context.Background(), signalbox.ListInput{
		Target: signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"},
	})
	if err != nil || len(page.Signals) != 1 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	settings.LeaseID = "lease.wrong"
	if _, err := service.ConfigureHost(context.Background(), settings); err == nil {
		t.Fatal("wrong Host lease configured signals")
	}
}

func testRegistration() controlplane.HostRegistration {
	return controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion, HostID: "host.one",
		InstanceID: "instance.one", LeaseTTLMillis: 5_000,
		Manifest: host.HostManifest{
			ContractVersion: host.ContractVersion, AdapterID: "test.adapter", AdapterVersion: "1.0.0",
			EngineID: "test.engine", EngineVersion: "1", Runtime: "go", Platform: "test",
			Headless: true, Authority: host.AuthorityServer,
			Deployment: host.DeploymentLoopbackSidecar, Control: host.ControlSemantic,
			ClockModes:          []host.ClockMode{host.ClockStep},
			DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
			MaxConcurrentActors: 4,
			Durability:          host.Durability{Profile: host.DurabilityAdvisory, StableIdentity: true},
		},
	}
}

func testPublication(epoch host.Epoch, source controlplane.DecisionSource) controlplane.WorldPublication {
	authority := &controlplane.DecisionAuthority{
		Source: source, Revision: 1, PersonaMode: controlplane.PersonaCharacterBound,
	}
	if source == controlplane.DecisionExternal {
		authority.ControllerPrincipalID = "player.one"
	}
	return controlplane.WorldPublication{
		WorldID: "world.one", DisplayName: "World", Sequence: 1,
		Actors: []controlplane.ActorPublication{{
			ActorID: "actor.one", OwnerPrincipalID: "player.one", DisplayName: "Companion",
			ObservationSeq: 2, Epoch: epoch, Authority: authority,
			State: json.RawMessage(`{"status":"ready"}`),
		}},
	}
}

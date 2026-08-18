package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
)

func TestServicePublishesPrincipalFilteredDefensiveViews(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	service := New(Options{
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)),
	})
	lease := mustRegister(t, service, registration("instance.one"))
	publication := worldPublication(1, "ready")
	if err := service.PublishWorld("test.host", lease.LeaseID, publication); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}

	publication.Actors[0].State[2] = 'x'

	owner := host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{ScopeActorRead},
	}
	worlds, err := service.ListWorlds(owner)
	if err != nil || len(worlds) != 1 || !worlds[0].Online {
		t.Fatalf("ListWorlds = %#v, %v", worlds, err)
	}
	actors, err := service.ListActors(owner, "test.host", "world.one")
	if err != nil || len(actors) != 1 {
		t.Fatalf("ListActors = %#v, %v", actors, err)
	}
	if string(actors[0].State) != `{"status":"ready"}` {
		t.Fatalf("stored actor state was mutated: %s", actors[0].State)
	}
	actors[0].State[2] = 'x'
	actor, err := service.GetActor(owner, "test.host", "world.one", "actor.one")
	if err != nil || string(actor.State) != `{"status":"ready"}` {
		t.Fatalf("GetActor = %#v, %v", actor, err)
	}
	stranger := host.Principal{
		ID:            "player.two",
		GrantedScopes: []string{ScopeActorRead},
	}
	hidden, err := service.ListWorlds(stranger)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("stranger ListWorlds = %#v, %v", hidden, err)
	}
	if _, err := service.GetActor(
		stranger, "test.host", "world.one", "actor.one",
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger GetActor error = %v", err)
	}

	admin := host.Principal{
		ID:            "local.admin",
		GrantedScopes: []string{ScopeHostAdmin},
	}
	if visible, err := service.ListActors(
		admin, "test.host", "world.one",
	); err != nil || len(visible) != 1 {
		t.Fatalf("admin ListActors = %#v, %v", visible, err)
	}
}

func TestServiceKeepsEmptyWorldVisibleToDeclaredPrincipals(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{21}, 64)),
	})
	lease := mustRegister(t, service, registration("instance.empty-world"))
	publication := worldPublication(1, "ready")
	publication.VisiblePrincipalIDs = []string{"player.one", "agent.one"}
	if err := service.PublishWorld("test.host", lease.LeaseID, publication); err != nil {
		t.Fatalf("initial PublishWorld: %v", err)
	}
	publication.Sequence = 2
	publication.Actors = nil
	if err := service.PublishWorld("test.host", lease.LeaseID, publication); err != nil {
		t.Fatalf("empty PublishWorld: %v", err)
	}

	for _, principalID := range []string{"player.one", "agent.one"} {
		principal := host.Principal{
			ID: principalID, GrantedScopes: []string{ScopeActorRead},
		}
		worlds, err := service.ListWorlds(principal)
		if err != nil || len(worlds) != 1 || !worlds[0].Online {
			t.Fatalf("ListWorlds(%s) = %#v, %v", principalID, worlds, err)
		}
		actors, err := service.ListActors(principal, "test.host", "world.one")
		if err != nil || len(actors) != 0 {
			t.Fatalf("ListActors(%s) = %#v, %v", principalID, actors, err)
		}
	}
	for _, principal := range []host.Principal{
		{ID: "other.one", GrantedScopes: []string{ScopeActorRead}},
		{ID: "player.one"},
	} {
		worlds, err := service.ListWorlds(principal)
		if err != nil || len(worlds) != 0 {
			t.Fatalf("hidden ListWorlds(%s) = %#v, %v", principal.ID, worlds, err)
		}
	}
	adminWorlds, err := service.ListWorlds(host.Principal{
		ID: "admin.one", GrantedScopes: []string{ScopeHostAdmin},
	})
	if err != nil || len(adminWorlds) != 1 {
		t.Fatalf("admin ListWorlds = %#v, %v", adminWorlds, err)
	}
}

func TestServiceLeaseConflictExpiryAndTakeover(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	service := New(Options{
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{2}, 64)),
	})
	lease := mustRegister(t, service, registration("instance.one"))
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, worldPublication(1, "ready"),
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	if _, err := service.RegisterHost(
		registration("instance.two"),
	); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("live takeover error = %v", err)
	}
	renewed, err := service.RenewHost("test.host", lease.LeaseID)
	if err != nil || renewed.LeaseID != lease.LeaseID {
		t.Fatalf("RenewHost = %#v, %v", renewed, err)
	}

	now = now.Add(6 * time.Second)
	owner := host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{ScopeActorRead},
	}
	actor, err := service.GetActor(owner, "test.host", "world.one", "actor.one")
	if err != nil || actor.Online {
		t.Fatalf("expired GetActor = %#v, %v", actor, err)
	}
	if _, err := service.RenewHost(
		"test.host", lease.LeaseID,
	); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired RenewHost error = %v", err)
	}

	replacement, err := service.RegisterHost(registration("instance.two"))
	if err != nil || replacement.InstanceID != "instance.two" ||
		replacement.ExpiresAtUnixMillis <= now.UnixMilli() {
		t.Fatalf("replacement lease = %#v, %v", replacement, err)
	}
	worlds, err := service.ListWorlds(host.Principal{
		ID:            "local.admin",
		GrantedScopes: []string{ScopeHostAdmin},
	})
	if err != nil || len(worlds) != 0 {
		t.Fatalf("replacement retained stale worlds = %#v, %v", worlds, err)
	}
}

func TestUnregisterHostWakesBlockedPoll(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{7}, 64)),
	})
	lease := mustRegister(t, service, registration("instance.polling"))
	service.mu.RLock()
	changed := service.changed
	service.mu.RUnlock()

	if err := service.UnregisterHost("test.host", lease.LeaseID); err != nil {
		t.Fatalf("UnregisterHost: %v", err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("UnregisterHost did not wake blocked Host polls")
	}
	if _, err := service.PollHost(
		context.Background(),
		"test.host",
		lease.LeaseID,
		1,
	); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("PollHost after unregister error = %v", err)
	}
}

func TestServicePublicationSequenceIsIdempotent(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{3}, 64)),
	})
	lease := mustRegister(t, service, registration("instance.one"))
	first := worldPublication(1, "ready")
	if err := service.PublishWorld("test.host", lease.LeaseID, first); err != nil {
		t.Fatalf("first PublishWorld: %v", err)
	}
	if err := service.PublishWorld("test.host", lease.LeaseID, first); err != nil {
		t.Fatalf("idempotent PublishWorld: %v", err)
	}
	changed := worldPublication(1, "changed")
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, changed,
	); !errors.Is(err, ErrStale) {
		t.Fatalf("same-sequence changed publication error = %v", err)
	}
	next := worldPublication(2, "changed")
	next.Actors[0].ObservationSeq = 2
	if err := service.PublishWorld("test.host", lease.LeaseID, next); err != nil {
		t.Fatalf("next PublishWorld: %v", err)
	}
}

func TestServiceWaitActorUsesPublishedCursor(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{31}, 64)),
	})
	lease := mustRegister(t, service, registration("instance.one"))
	first := worldPublication(1, "ready")
	if err := service.PublishWorld("test.host", lease.LeaseID, first); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	owner := host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{ScopeActorRead},
	}
	current, err := service.GetActor(
		owner, "test.host", "world.one", "actor.one",
	)
	if err != nil {
		t.Fatalf("GetActor: %v", err)
	}
	result := make(chan ActorUpdate, 1)
	failures := make(chan error, 1)
	go func() {
		update, waitErr := service.WaitActor(
			context.Background(),
			owner,
			WaitActorInput{
				HostID:                 "test.host",
				WorldID:                "world.one",
				ActorID:                "actor.one",
				AfterObservationSeq:    current.ObservationSeq,
				AfterAuthorityRevision: current.Authority.Revision,
				WaitMillis:             1_000,
			},
		)
		if waitErr != nil {
			failures <- waitErr
			return
		}
		result <- update
	}()
	time.Sleep(10 * time.Millisecond)
	next := worldPublication(2, "working")
	next.Actors[0].ObservationSeq = 2
	if err := service.PublishWorld("test.host", lease.LeaseID, next); err != nil {
		t.Fatalf("second PublishWorld: %v", err)
	}
	select {
	case waitErr := <-failures:
		t.Fatalf("WaitActor: %v", waitErr)
	case update := <-result:
		if !update.Changed || update.Actor.ObservationSeq != 2 ||
			string(update.Actor.State) != `{"status":"working"}` {
			t.Fatalf("WaitActor = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitActor did not wake after publication")
	}
	unchanged, err := service.WaitActor(
		context.Background(),
		owner,
		WaitActorInput{
			HostID:                 "test.host",
			WorldID:                "world.one",
			ActorID:                "actor.one",
			AfterObservationSeq:    2,
			AfterAuthorityRevision: current.Authority.Revision,
			WaitMillis:             1,
		},
	)
	if err != nil || unchanged.Changed {
		t.Fatalf("unchanged WaitActor = %#v, %v", unchanged, err)
	}
	if _, err := service.WaitActor(
		context.Background(),
		owner,
		WaitActorInput{WaitMillis: 25_001},
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized WaitActor error = %v", err)
	}
}

func TestServiceRejectsAmbiguousOrUnboundPublication(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{4}, 64)),
	})
	lease := mustRegister(t, service, registration("instance.one"))

	duplicateJSON := worldPublication(1, "ready")
	duplicateJSON.Actors[0].State =
		json.RawMessage(`{"status":"ready","status":"changed"}`)
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, duplicateJSON,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate JSON error = %v", err)
	}

	missingAuthority := worldPublication(1, "ready")
	missingAuthority.Actors[0].Authority = nil
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, missingAuthority,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing decision authority error = %v", err)
	}

	duplicateActor := worldPublication(1, "ready")
	duplicateActor.Actors = append(
		duplicateActor.Actors, duplicateActor.Actors[0],
	)
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, duplicateActor,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate actor error = %v", err)
	}

	duplicatePrincipal := worldPublication(1, "ready")
	duplicatePrincipal.VisiblePrincipalIDs = []string{"player.one", "player.one"}
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, duplicatePrincipal,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate visible principal error = %v", err)
	}
}

func registration(instanceID string) HostRegistration {
	return HostRegistration{
		ContractVersion: ContractVersion,
		HostID:          "test.host",
		InstanceID:      instanceID,
		LeaseTTLMillis:  5_000,
		Manifest: host.HostManifest{
			ContractVersion:     host.ContractVersion,
			AdapterID:           "test.adapter",
			AdapterVersion:      "1.0.0",
			EngineID:            "test.engine",
			EngineVersion:       "1",
			Runtime:             "go",
			Platform:            "test",
			Headless:            true,
			Authority:           host.AuthorityServer,
			Deployment:          host.DeploymentLoopbackSidecar,
			Control:             host.ControlSemantic,
			ClockModes:          []host.ClockMode{host.ClockStep},
			DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
			MaxConcurrentActors: 4,
			Durability: host.Durability{
				Profile:        host.DurabilityAdvisory,
				StableIdentity: true,
			},
		},
	}
}

func worldPublication(sequence uint64, status string) WorldPublication {
	epoch := host.Epoch{
		SessionID: "session.one",
		WorldID:   "world.one",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
	return WorldPublication{
		WorldID:     "world.one",
		DisplayName: "Test World",
		Sequence:    sequence,
		Actors: []ActorPublication{{
			ActorID:          "actor.one",
			OwnerPrincipalID: "player.one",
			DisplayName:      "Companion",
			ObservationSeq:   1,
			Epoch:            epoch,
			Authority: &DecisionAuthority{
				Source:                DecisionExternal,
				ControllerPrincipalID: "player.one",
				Revision:              1,
				PersonaMode:           PersonaCharacterBound,
			},
			State: json.RawMessage(
				`{"status":"` + status + `"}`,
			),
		}},
	}
}

func mustRegister(
	t *testing.T,
	service *Service,
	request HostRegistration,
) HostLease {
	t.Helper()
	lease, err := service.RegisterHost(request)
	if err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}
	return lease
}

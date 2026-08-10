package cognition_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/cognition"
)

func TestLocalPersonaProviderUsesControllerBindingThenActorFallback(t *testing.T) {
	provider, err := cognition.NewLocalPersonaProvider(
		[]cognition.PersonaProfile{
			{
				PersonaID: "persona.shared", Version: "v1", Identity: "Shared identity.",
			},
			{
				PersonaID: "persona.external", Version: "v1", Identity: "External identity.",
			},
		},
		[]cognition.PersonaBinding{
			{ActorID: "actor.mira", PersonaID: "persona.shared", Version: "v1"},
			{
				ActorID: "actor.mira", ControllerID: "controller.external",
				PersonaID: "persona.external", Version: "v1",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	external, err := provider.Load(context.Background(), cognition.PersonaRequest{
		ActorID: "actor.mira", ControllerID: "controller.external",
	})
	if err != nil {
		t.Fatal(err)
	}
	if external.PersonaID != "persona.external" {
		t.Fatalf("controller binding was not selected: %+v", external)
	}

	fallback, err := provider.Load(context.Background(), cognition.PersonaRequest{
		ActorID: "actor.mira", ControllerID: "controller.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.PersonaID != "persona.shared" {
		t.Fatalf("actor fallback was not selected: %+v", fallback)
	}
}

func TestLocalPersonaProviderReturnsImmutableValuesAndSnapshot(t *testing.T) {
	profile := cognition.PersonaProfile{
		PersonaID: "persona.mira",
		Version:   "v1",
		Identity:  "Mira",
		Traits:    []string{"curious"},
		Initiative: cognition.InitiativePolicy{
			Enabled:  true,
			Triggers: []string{"player.nearby"},
		},
	}
	provider, err := cognition.NewLocalPersonaProvider(
		[]cognition.PersonaProfile{profile},
		[]cognition.PersonaBinding{{
			ActorID: "actor.mira", PersonaID: "persona.mira", Version: "v1",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	profile.Traits[0] = "mutated-input"
	loaded, err := provider.Load(context.Background(), cognition.PersonaRequest{
		ActorID: "actor.mira", ControllerID: "controller.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded.Traits[0] = "mutated-output"
	loaded.Initiative.Triggers[0] = "mutated-output"

	reloaded, err := provider.Load(context.Background(), cognition.PersonaRequest{
		ActorID: "actor.mira", ControllerID: "controller.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Traits, []string{"curious"}) ||
		!reflect.DeepEqual(reloaded.Initiative.Triggers, []string{"player.nearby"}) {
		t.Fatalf("provider state was mutated through a returned value: %+v", reloaded)
	}

	snapshot, err := provider.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || len(snapshot.Profiles) != 1 || len(snapshot.Bindings) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestPersonaProviderRejectsInvalidOrMissingBindings(t *testing.T) {
	_, err := cognition.NewLocalPersonaProvider(
		[]cognition.PersonaProfile{{PersonaID: "persona.mira", Version: "v1", Identity: "Mira"}},
		[]cognition.PersonaBinding{{
			ActorID: "actor.mira", PersonaID: "persona.missing", Version: "v1",
		}},
	)
	if !errors.Is(err, cognition.ErrProviderNotFound) {
		t.Fatalf("expected missing profile rejection, got %v", err)
	}

	provider, err := cognition.NewLocalPersonaProvider(
		[]cognition.PersonaProfile{{PersonaID: "persona.mira", Version: "v1", Identity: "Mira"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Load(context.Background(), cognition.PersonaRequest{
		ActorID: "actor.mira", ControllerID: "controller.internal",
	})
	if !errors.Is(err, cognition.ErrProviderNotFound) {
		t.Fatalf("expected missing binding error, got %v", err)
	}
}

func TestPersonaProviderHonorsCancellation(t *testing.T) {
	provider, err := cognition.NewLocalPersonaProvider(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = provider.Load(ctx, cognition.PersonaRequest{
		ActorID: "actor.mira", ControllerID: "controller.internal",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled load, got %v", err)
	}
}

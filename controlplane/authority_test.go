package controlplane

import (
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/host"
)

func TestDecisionAuthorityValidation(t *testing.T) {
	tests := []struct {
		name      string
		authority DecisionAuthority
	}{
		{
			name: "internal principal",
			authority: DecisionAuthority{
				Source:                DecisionInternal,
				ControllerPrincipalID: "player.one",
				Revision:              1,
				PersonaMode:           PersonaCharacterBound,
			},
		},
		{
			name: "internal avatar",
			authority: DecisionAuthority{
				Source:      DecisionInternal,
				Revision:    1,
				PersonaMode: PersonaAgentAvatar,
			},
		},
		{
			name: "external missing principal",
			authority: DecisionAuthority{
				Source:      DecisionExternal,
				Revision:    1,
				PersonaMode: PersonaCharacterBound,
			},
		},
		{
			name: "zero revision",
			authority: DecisionAuthority{
				Source:                DecisionExternal,
				ControllerPrincipalID: "player.one",
				PersonaMode:           PersonaCharacterBound,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publication := worldPublication(1, "ready")
			publication.Actors[0].Authority = &test.authority
			if err := validatePublication(
				publication,
				registration("instance.authority").Manifest,
			); err == nil {
				t.Fatal("invalid authority was accepted")
			}
		})
	}
}

func TestLegacyPublicationPreservesExternalOwnerControl(t *testing.T) {
	service, _, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorRead, ScopeActorExecute)
	actors, err := service.ListActors(
		principal,
		"test.host",
		"world.one",
	)
	if err != nil || len(actors) != 1 {
		t.Fatalf("ListActors = %#v, %v", actors, err)
	}
	authority := actors[0].Authority
	if authority.Source != DecisionExternal ||
		authority.ControllerPrincipalID != principal.ID ||
		authority.Revision != 1 ||
		authority.PersonaMode != PersonaCharacterBound {
		t.Fatalf("legacy authority = %#v", authority)
	}
	offers, err := service.ListActorOffers(
		principal,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || len(offers) != 1 {
		t.Fatalf("ListActorOffers = %#v, %v", offers, err)
	}
}

func TestInternalAuthorityBlocksExternalOfferSelection(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	publication := worldPublication(2, "internal")
	publication.Actors[0].Authority = &DecisionAuthority{
		Source:      DecisionInternal,
		Revision:    2,
		PersonaMode: PersonaCharacterBound,
	}
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	principal := operationPrincipal(ScopeActorRead, ScopeActorExecute)
	if _, err := service.ListActorOffers(
		principal,
		"test.host",
		"world.one",
		"actor.one",
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListActorOffers error = %v", err)
	}
	if _, err := service.ExecuteActorOffer(
		principal,
		ExecuteOfferInput{
			RequestID: "request.internal.offer",
			HostID:    "test.host",
			WorldID:   "world.one",
			ActorID:   "actor.one",
			OfferID:   "offer.follow",
		},
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ExecuteActorOffer error = %v", err)
	}
}

func TestAuthorityRevisionFencesUnacceptedOperations(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorExecute)
	operation, err := service.ExecuteActorOffer(
		principal,
		ExecuteOfferInput{
			RequestID: "request.authority.fence",
			HostID:    "test.host",
			WorldID:   "world.one",
			ActorID:   "actor.one",
			OfferID:   "offer.follow",
		},
	)
	if err != nil {
		t.Fatalf("ExecuteActorOffer: %v", err)
	}

	publication := worldPublication(2, "switched")
	publication.Actors[0].Authority = &DecisionAuthority{
		Source:      DecisionInternal,
		Revision:    2,
		PersonaMode: PersonaCharacterBound,
	}
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationStale {
		t.Fatalf("fenced operation = %#v, %v", view, err)
	}
}

func TestExternalAuthorityRequiresBoundPrincipal(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	publication := worldPublication(2, "external")
	publication.Actors[0].Authority = &DecisionAuthority{
		Source:                DecisionExternal,
		ControllerPrincipalID: "agent.one",
		Revision:              2,
		PersonaMode:           PersonaAgentAvatar,
	}
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	admin := host.Principal{
		ID:            "admin.one",
		GrantedScopes: []string{ScopeHostAdmin, ScopeActorExecute},
	}
	if _, err := service.ListActorOffers(
		admin,
		"test.host",
		"world.one",
		"actor.one",
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unbound admin error = %v", err)
	}
	controller := host.Principal{
		ID: "agent.one",
		GrantedScopes: []string{
			ScopeActorRead,
			ScopeActorSpeak,
			ScopeActorExecute,
		},
	}
	actors, err := service.ListActors(
		controller,
		"test.host",
		"world.one",
	)
	if err != nil || len(actors) != 1 {
		t.Fatalf("bound controller actors = %#v, %v", actors, err)
	}
	operation, err := service.ExecuteActorOffer(
		controller,
		ExecuteOfferInput{
			RequestID: "request.agent.offer",
			HostID:    "test.host",
			WorldID:   "world.one",
			ActorID:   "actor.one",
			OfferID:   "offer.follow",
			TurnID:    "turn.agent.one",
		},
	)
	if err != nil || operation.Kind != ControlOffer ||
		operation.TurnID != "turn.agent.one" {
		t.Fatalf("bound controller offer = %#v, %v", operation, err)
	}
}

func TestExternalControllerQueuesBoundActorUtterance(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorSpeak)
	input := ActorUtteranceInput{
		RequestID: "request.utterance.one",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		TurnID:    "turn.one",
		Text:      "I noticed you have been building for a while.",
	}
	operation, err := service.SubmitActorUtterance(principal, input)
	if err != nil || operation.Kind != ControlUtterance ||
		operation.TurnID != input.TurnID {
		t.Fatalf("SubmitActorUtterance = %#v, %v", operation, err)
	}
	retried, err := service.SubmitActorUtterance(principal, input)
	if err != nil || retried.OperationID != operation.OperationID {
		t.Fatalf("idempotent utterance = %#v, %v", retried, err)
	}
	changed := input
	changed.TurnID = "turn.two"
	if _, err := service.SubmitActorUtterance(
		principal,
		changed,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed turn id error = %v", err)
	}

	batch := pollHost(t, service, lease, 1)
	if len(batch.Requests) != 1 {
		t.Fatalf("utterance batch = %#v", batch)
	}
	request := batch.Requests[0].Request
	if request.Kind != ControlUtterance ||
		request.TurnID != input.TurnID ||
		request.Text != input.Text ||
		request.Binding == nil ||
		request.Binding.AuthorityRevision != 1 {
		t.Fatalf("utterance request = %#v", request)
	}
}

func TestActorUtteranceRejectsInactiveSourceAndLongText(t *testing.T) {
	service, lease, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorSpeak)
	input := ActorUtteranceInput{
		RequestID: "request.utterance.invalid",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		TurnID:    "turn.invalid",
		Text:      strings.Repeat("a", maxUtteranceRunes+1),
	}
	if _, err := service.SubmitActorUtterance(
		principal,
		input,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long utterance error = %v", err)
	}

	publication := worldPublication(2, "internal")
	publication.Actors[0].Authority = &DecisionAuthority{
		Source:      DecisionInternal,
		Revision:    2,
		PersonaMode: PersonaCharacterBound,
	}
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	input.RequestID = "request.utterance.inactive"
	input.Text = "This source is inactive."
	if _, err := service.SubmitActorUtterance(
		principal,
		input,
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("inactive source error = %v", err)
	}
}

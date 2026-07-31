package controlplane

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestHTTPClientUsesDaemonBoundPrincipal(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(sequenceBytes(512)),
	})
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorConverse,
		ScopeActorDirect,
		ScopeActorSpeak,
		ScopeActorExecute,
		ScopeOperationCancel,
	)
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token:           testControlToken,
		ClientPrincipal: &principal,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	lease := mustRegister(t, service, registration("instance.http-client"))
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		worldPublication(1, "ready"),
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	client, err := NewHTTPClient(
		"http://127.0.0.1:7375",
		testControlToken,
	)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	client.client = &http.Client{
		Transport: handlerRoundTripper{handler: handler},
	}
	ctx := context.Background()
	info, err := client.Info(ctx)
	expectedPrincipal := clonePrincipalValue(principal)
	if err != nil ||
		info.ContractVersion != ContractVersion ||
		!reflect.DeepEqual(info.Principal, expectedPrincipal) {
		t.Fatalf("Info = %#v, %v", info, err)
	}
	worlds, err := client.ListWorlds(ctx)
	if err != nil || len(worlds) != 1 || worlds[0].WorldID != "world.one" {
		t.Fatalf("ListWorlds = %#v, %v", worlds, err)
	}
	actors, err := client.ListActors(ctx, "test.host", "world.one")
	if err != nil || len(actors) != 1 || actors[0].ActorID != "actor.one" {
		t.Fatalf("ListActors = %#v, %v", actors, err)
	}
	actor, err := client.GetActor(
		ctx,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || actor.ObservationSeq != 1 {
		t.Fatalf("GetActor = %#v, %v", actor, err)
	}
	update, err := client.WaitActor(ctx, WaitActorInput{
		HostID:                 "test.host",
		WorldID:                "world.one",
		ActorID:                "actor.one",
		AfterObservationSeq:    actor.ObservationSeq,
		AfterAuthorityRevision: actor.Authority.Revision,
		WaitMillis:             0,
	})
	if err != nil || update.Changed ||
		update.Actor.ObservationSeq != actor.ObservationSeq {
		t.Fatalf("WaitActor = %#v, %v", update, err)
	}
	offers, err := client.ListActorOffers(
		ctx,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || len(offers) != 1 || offers[0].OfferID != "offer.follow" {
		t.Fatalf("ListActorOffers = %#v, %v", offers, err)
	}

	message, err := client.SendActorMessage(ctx, ActorTextInput{
		RequestID: "request.client.message",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Hello from the thin client.",
	})
	if err != nil || message.Status != OperationQueued {
		t.Fatalf("SendActorMessage = %#v, %v", message, err)
	}
	operationUpdate, err := client.WaitOperation(ctx, WaitOperationInput{
		OperationID: message.OperationID,
		AfterCursor: message.Cursor,
		WaitMillis:  0,
	})
	if err != nil || operationUpdate.Changed ||
		operationUpdate.Operation.Terminal ||
		operationUpdate.Operation.ExecutionConfirmed {
		t.Fatalf("WaitOperation = %#v, %v", operationUpdate, err)
	}
	utterance, err := client.SubmitActorUtterance(ctx, ActorUtteranceInput{
		RequestID: "request.client.utterance",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		TurnID:    "turn.client.one",
		Text:      "I am ready.",
	})
	if err != nil || utterance.Kind != ControlUtterance ||
		utterance.TurnID != "turn.client.one" {
		t.Fatalf("SubmitActorUtterance = %#v, %v", utterance, err)
	}
	view, err := client.GetOperation(ctx, message.OperationID)
	if err != nil || view.OperationID != message.OperationID {
		t.Fatalf("GetOperation = %#v, %v", view, err)
	}
	cancelled, err := client.CancelOperation(ctx, message.OperationID)
	if err != nil || cancelled.Status != OperationCancelled {
		t.Fatalf("CancelOperation = %#v, %v", cancelled, err)
	}
	offerOperation, err := client.ExecuteActorOffer(ctx, ExecuteOfferInput{
		RequestID: "request.client.offer",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		OfferID:   "offer.follow",
	})
	if err != nil || offerOperation.Kind != ControlOffer {
		t.Fatalf("ExecuteActorOffer = %#v, %v", offerOperation, err)
	}
}

type handlerRoundTripper struct {
	handler http.Handler
}

func (transport handlerRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

func TestHTTPClientRejectsInvalidOriginAndCredentials(t *testing.T) {
	for _, target := range []string{
		"https://127.0.0.1:7375",
		"http://example.com:7375",
		"http://user@127.0.0.1:7375",
		"http://127.0.0.1:7375/path",
	} {
		if _, err := NewHTTPClient(target, testControlToken); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewHTTPClient(%q) error = %v", target, err)
		}
	}
	if _, err := NewHTTPClient(
		"http://127.0.0.1:7375",
		"short",
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short token error = %v", err)
	}
}

func TestHTTPClientCannotBypassDaemonToken(t *testing.T) {
	service := New(Options{})
	principal := readPrincipal()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token:           testControlToken,
		ClientPrincipal: &principal,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	client, err := NewHTTPClient(
		"http://127.0.0.1:7375",
		"fedcba9876543210fedcba9876543210",
	)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	client.client = &http.Client{
		Transport: handlerRoundTripper{handler: handler},
	}
	if _, err := client.Info(
		context.Background(),
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Info wrong-token error = %v", err)
	}
}

package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestAuthenticationAndHealth(t *testing.T) {
	server := newServer(t, httpapi.Options{Token: "secret-token"})
	health := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthResponse := httptest.NewRecorder()
	server.ServeHTTP(healthResponse, health)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("health status: %d", healthResponse.Code)
	}

	request := jsonRequest(t, "/v1/session/create", apiCreateRequest())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("expected bearer challenge, got %d %s", response.Code, response.Body.String())
	}

	request = jsonRequest(t, "/v1/session/create", apiCreateRequest())
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create status: %d %s", response.Code, response.Body.String())
	}
	assertResponseOK(t, response)
}

func TestStrictJSONAndBodyLimit(t *testing.T) {
	server := newServer(t, httpapi.Options{MaxBodyBytes: 256})
	request := httptest.NewRequest(http.MethodPost, "/v1/session/create", strings.NewReader(`{"protocol_version":"rin.protocol/v1","unexpected":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/session/create", strings.NewReader(`{"padding":"`+strings.Repeat("x", 400)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/session/create", strings.NewReader(`{}`))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content type status: %d", response.Code)
	}
}

func TestHTTPFlowAndNoSafeAction(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	response := perform(t, server, "/v1/session/create", apiCreateRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}

	propose := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.http",
		RequestID:       "propose.http",
		ActorID:         "npc.http",
		Tick:            0,
		Intent:          "Respond without exposing private data.",
		Tags:            []string{"private"},
		CandidateActions: []protocol.ActionSpec{{
			ID: "talk", Kind: "dialogue", Description: "answer the question",
		}},
	}
	response = perform(t, server, "/v1/agent/propose", propose)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe proposal: %d %s", response.Code, response.Body.String())
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != "no_safe_action" {
		t.Fatalf("unexpected error: %+v", envelope.Error)
	}
}

func newServer(t *testing.T, options httpapi.Options) http.Handler {
	t.Helper()
	engine, err := rinruntime.Open(store.NewMemory(), policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	return httpapi.New(engine, options)
}

func jsonRequest(t *testing.T, path string, value any) *http.Request {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	return request
}

func perform(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	request := jsonRequest(t, path, value)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertResponseOK(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Error != nil {
		t.Fatalf("unexpected response: %+v", envelope)
	}
}

func apiCreateRequest() protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.http",
		SessionID:       "session.http",
		Binding:         protocol.Binding{GameID: "game.http", ContentID: "base", ContentVersion: "1", ContentHash: "hash"},
		Actors: []protocol.ActorSeed{{
			ID: "npc.http", Kind: "npc", DisplayName: "HTTP NPC", ThinkEveryTicks: 1, Enabled: true,
			Boundaries: []protocol.Boundary{{ID: "boundary.private", Description: "Keep private data private.", TriggerTags: []string{"private"}, Response: "refuse"}},
			Goals:      []protocol.Goal{{ID: "goal.http", Description: "Respond", Priority: 1, TargetProgress: 1, Status: "active"}},
		}},
	}
}

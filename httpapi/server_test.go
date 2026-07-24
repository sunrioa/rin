package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/generation"
	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/jobs"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	"github.com/sunrioa/rin/provider"
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

func TestInvalidSnapshotMapsToBadRequest(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	response := perform(t, server, "/v1/session/restore", protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.invalid-snapshot",
		RequestID:       "restore.invalid-snapshot",
		ExpectedBinding: protocol.Binding{
			GameID: "game.http", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Snapshot: protocol.Snapshot{
			ProtocolVersion: protocol.Version,
			State: protocol.SessionState{
				SessionID: "session.invalid-snapshot",
				Binding: protocol.Binding{
					GameID: "game.http", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
				},
			},
		},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid snapshot status: %d %s", response.Code, response.Body.String())
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != "invalid_snapshot" {
		t.Fatalf("invalid snapshot error: %+v", envelope.Error)
	}
}

func TestOversizedInlineSnapshotMapsToPayloadTooLarge(t *testing.T) {
	binding := protocol.Binding{
		GameID: "game.http", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
	}
	server := newServer(t, httpapi.Options{})
	response := perform(t, server, "/v1/session/restore", protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.oversized-snapshot",
		RequestID:       "restore.oversized-snapshot",
		ExpectedBinding: binding,
		Snapshot: protocol.Snapshot{
			ProtocolVersion: protocol.Version,
			StateHash:       strings.Repeat("a", rinruntime.MaxInlineSnapshotBytes),
			State: protocol.SessionState{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.oversized-snapshot",
				Binding:         binding,
			},
		},
	})
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized snapshot status: %d %s", response.Code, response.Body.String())
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != "snapshot_too_large" {
		t.Fatalf("oversized snapshot error: %+v", envelope.Error)
	}
}

func TestDefaultTransportBudgetRoundTripsSnapshotLargerThanLegacyClientLimit(t *testing.T) {
	const legacyClientLimit = 2 << 20
	create := largeSnapshotCreateRequest("session.large-inline-snapshot")
	source := newServer(t, httpapi.Options{})
	if response := perform(t, source, "/v1/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("large create: %d %s", response.Code, response.Body.String())
	}
	snapshotResponse := perform(t, source, "/v1/session/snapshot", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	})
	if snapshotResponse.Code != http.StatusOK {
		t.Fatalf("large snapshot: %d %s", snapshotResponse.Code, snapshotResponse.Body.String())
	}
	if snapshotResponse.Body.Len() <= legacyClientLimit {
		t.Fatalf(
			"fixture snapshot response is %d bytes, want more than the legacy %d-byte client limit",
			snapshotResponse.Body.Len(),
			legacyClientLimit,
		)
	}
	if snapshotResponse.Body.Len() > 32<<20 {
		t.Fatalf("fixture exceeded the default client response budget: %d", snapshotResponse.Body.Len())
	}
	var envelope struct {
		OK   bool              `json:"ok"`
		Data protocol.Snapshot `json:"data"`
	}
	if err := json.Unmarshal(snapshotResponse.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("large snapshot envelope was not successful: %s", snapshotResponse.Body.String())
	}

	target := newServer(t, httpapi.Options{})
	restoreResponse := perform(t, target, "/v1/session/restore", protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "restore.large-inline-snapshot",
		ExpectedBinding: create.Binding,
		Snapshot:        envelope.Data,
	})
	if restoreResponse.Code != http.StatusOK {
		t.Fatalf("large snapshot restore: %d %s", restoreResponse.Code, restoreResponse.Body.String())
	}
}

func TestDefiniteCreateStorageFailureMapsToInternalServerError(t *testing.T) {
	eventStore := &definiteCreateFailureStore{Store: store.NewMemory()}
	engine, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	server := httpapi.New(engine, httpapi.Options{})
	response := perform(t, server, "/v1/session/create", apiCreateRequest())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("definite storage failure status: %d %s", response.Code, response.Body.String())
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != "store_create_failed" {
		t.Fatalf("definite storage failure error: %+v", envelope.Error)
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

func TestCommitHTTPReportsOutcomeAfterSessionAdvances(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	create := apiCreateRequest()
	create.Features = []string{protocol.FeatureOutcomeReporting}
	if response := perform(t, server, "/v1/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	proposeResponse := perform(t, server, "/v1/agent/propose", protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.http",
		RequestID:       "propose.outcome-report",
		ActorID:         "npc.http",
		Tick:            0,
		Intent:          "Wait for the game authority.",
		CandidateActions: []protocol.ActionSpec{{
			ID: "wait", Kind: "wait", Description: "wait",
		}},
	})
	if proposeResponse.Code != http.StatusOK {
		t.Fatalf("propose: %d %s", proposeResponse.Code, proposeResponse.Body.String())
	}
	var proposed struct {
		OK   bool                    `json:"ok"`
		Data protocol.ProposalResult `json:"data"`
	}
	if err := json.Unmarshal(proposeResponse.Body.Bytes(), &proposed); err != nil {
		t.Fatal(err)
	}
	if !proposed.OK || proposed.Data.Proposal.ID == "" {
		t.Fatalf("unexpected proposal response: %+v", proposed)
	}
	if response := perform(t, server, "/v1/session/observe", protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.http",
		RequestID:       "observe.after-apply",
		EventID:         "event.after-apply",
		Tick:            5,
		ObserverIDs:     []string{"npc.http"},
		Source:          "game",
		Kind:            "world",
		Summary:         "The authoritative game state advanced.",
		Importance:      1,
	}); response.Code != http.StatusOK {
		t.Fatalf("observe: %d %s", response.Code, response.Body.String())
	}
	commitResponse := perform(t, server, "/v1/action/commit", protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.http",
		RequestID:       "commit.outcome-report",
		ProposalID:      proposed.Data.Proposal.ID,
		EventID:         "event.outcome-report",
		Tick:            0,
		Accepted:        true,
		Outcome:         "The game had already applied this action.",
	})
	if commitResponse.Code != http.StatusOK {
		t.Fatalf("late outcome report: %d %s", commitResponse.Code, commitResponse.Body.String())
	}
	assertResponseOK(t, commitResponse)
}

func TestBatchCommitHTTPHandlesLateAndMixedBaseOutcomes(t *testing.T) {
	t.Run("late outcome", func(t *testing.T) {
		server := newServer(t, httpapi.Options{})
		create := apiCreateRequest()
		create.Features = []string{protocol.FeatureArbitration, protocol.FeatureOutcomeReporting}
		if response := perform(t, server, "/v1/session/create", create); response.Code != http.StatusOK {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		proposal := proposeHTTP(t, server, protocol.ProposeRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "propose.batch-http-late",
			ActorID:         "npc.http",
			Tick:            0,
			Intent:          "Wait for the game authority.",
			CandidateActions: []protocol.ActionSpec{{
				ID: "wait", Kind: "wait", Description: "wait",
			}},
		})
		if response := perform(t, server, "/v1/session/observe", protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "observe.batch-http-advance",
			EventID:         "event.batch-http-advance",
			Tick:            5,
			ObserverIDs:     []string{"npc.http"},
			Source:          "game",
			Kind:            "world",
			Summary:         "The authoritative game state advanced.",
			Importance:      1,
		}); response.Code != http.StatusOK {
			t.Fatalf("observe: %d %s", response.Code, response.Body.String())
		}
		response := perform(t, server, "/v1/action/commit-batch", protocol.BatchCommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "commit.batch-http-late",
			Tick:            0,
			Items: []protocol.CommitItem{{
				ProposalID: proposal.ID,
				EventID:    "event.batch-http-late",
				Accepted:   true,
				Outcome:    "The game had already applied this batch item.",
			}},
		})
		if response.Code != http.StatusOK {
			t.Fatalf("late batch outcome: %d %s", response.Code, response.Body.String())
		}
		assertResponseOK(t, response)

		stateResponse := perform(t, server, "/v1/session/get", protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
		})
		var stateEnvelope struct {
			Data protocol.SessionState `json:"data"`
		}
		if err := json.Unmarshal(stateResponse.Body.Bytes(), &stateEnvelope); err != nil {
			t.Fatal(err)
		}
		if stateEnvelope.Data.Tick != 5 ||
			stateEnvelope.Data.Proposals[proposal.ID].Status != "accepted" {
			t.Fatalf("late batch regressed HTTP state: %+v", stateEnvelope.Data)
		}
	})

	t.Run("mixed bases", func(t *testing.T) {
		server := newServer(t, httpapi.Options{})
		create := apiCreateRequest()
		create.SessionID = "session.http-mixed-base"
		create.RequestID = "create.http-mixed-base"
		create.Features = []string{protocol.FeatureArbitration, protocol.FeatureOutcomeReporting}
		other := create.Actors[0]
		other.ID = "npc.other"
		other.DisplayName = "Other HTTP NPC"
		create.Actors = append(create.Actors, other)
		if response := perform(t, server, "/v1/session/create", create); response.Code != http.StatusOK {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		older := proposeHTTP(t, server, protocol.ProposeRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "propose.http-base-one",
			ActorID:         "npc.http",
			Tick:            0,
			Intent:          "Wait.",
			CandidateActions: []protocol.ActionSpec{{
				ID: "wait", Kind: "wait", Description: "wait",
			}},
		})
		if response := perform(t, server, "/v1/session/observe", protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "observe.http-new-base",
			EventID:         "event.http-new-base",
			Tick:            5,
			ObserverIDs:     []string{"npc.http", "npc.other"},
			Source:          "game",
			Kind:            "world",
			Summary:         "The authoritative world revision advanced.",
			Importance:      1,
		}); response.Code != http.StatusOK {
			t.Fatalf("observe: %d %s", response.Code, response.Body.String())
		}
		newer := proposeHTTP(t, server, protocol.ProposeRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "propose.http-base-two",
			ActorID:         "npc.other",
			Tick:            5,
			Intent:          "Wait.",
			CandidateActions: []protocol.ActionSpec{{
				ID: "wait", Kind: "wait", Description: "wait",
			}},
		})
		response := perform(t, server, "/v1/action/commit-batch", protocol.BatchCommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "commit.http-mixed-base",
			Tick:            5,
			Items: []protocol.CommitItem{
				{ProposalID: older.ID, EventID: "event.http-old-base", Accepted: true, Outcome: "Old base."},
				{ProposalID: newer.ID, EventID: "event.http-new-base-outcome", Accepted: true, Outcome: "New base."},
			},
		})
		if response.Code != http.StatusConflict {
			t.Fatalf("mixed-base batch: %d %s", response.Code, response.Body.String())
		}
		var envelope protocol.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil || envelope.Error.Code != "proposal_base_mismatch" {
			t.Fatalf("unexpected mixed-base error: %+v", envelope.Error)
		}
	})
}

func TestTimelineAndReplayHTTPFlow(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	if response := perform(t, server, "/v1/session/create", apiCreateRequest()); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	timelineResponse := perform(t, server, "/v1/session/timeline", protocol.TimelineRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.http", Limit: 10,
	})
	if timelineResponse.Code != http.StatusOK {
		t.Fatalf("timeline: %d %s", timelineResponse.Code, timelineResponse.Body.String())
	}
	var timeline struct {
		OK   bool                      `json:"ok"`
		Data protocol.TimelineResponse `json:"data"`
	}
	if err := json.Unmarshal(timelineResponse.Body.Bytes(), &timeline); err != nil {
		t.Fatal(err)
	}
	if !timeline.OK || len(timeline.Data.Entries) != 1 || timeline.Data.Entries[0].Type != rinruntime.EventSessionCreated {
		t.Fatalf("unexpected timeline: %+v", timeline)
	}
	replayResponse := perform(t, server, "/v1/session/replay", protocol.ReplayRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.http", Revision: 1,
	})
	if replayResponse.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replayResponse.Code, replayResponse.Body.String())
	}
	var replay struct {
		OK   bool              `json:"ok"`
		Data protocol.Snapshot `json:"data"`
	}
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if !replay.OK || replay.Data.State.Revision != 1 || replay.Data.StateHash == "" {
		t.Fatalf("unexpected replay: %+v", replay)
	}
}

func TestAsyncProposalJobHTTPFlow(t *testing.T) {
	engine, err := rinruntime.Open(store.NewMemory(), policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(engine, jobs.Config{Workers: 1, QueueSize: 4, MaxJobs: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	server := httpapi.New(engine, httpapi.Options{Jobs: manager, PolicyMode: "deterministic"})
	response := perform(t, server, "/v1/session/create", apiCreateRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	input := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.http", RequestID: "job.http",
		ActorID: "npc.http", Tick: 0, Intent: "Respond",
		CandidateActions: []protocol.ActionSpec{{ID: "talk", Kind: "dialogue", Description: "answer"}},
	}
	response = perform(t, server, "/v1/jobs/propose", input)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", response.Code, response.Body.String())
	}
	var submitted struct {
		OK   bool                           `json:"ok"`
		Data protocol.ProposalJobSubmission `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+submitted.Data.JobID, nil)
		response = httptest.NewRecorder()
		server.ServeHTTP(response, request)
		var result struct {
			OK   bool                 `json:"ok"`
			Data protocol.ProposalJob `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.Status == "succeeded" {
			if result.Data.Proposal == nil || result.Data.Proposal.Action.ID != "talk" {
				t.Fatalf("unexpected proposal job: %+v", result.Data)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("async HTTP job did not finish")
}

func TestStructuredGenerationHTTPFlow(t *testing.T) {
	engine, err := rinruntime.Open(store.NewMemory(), policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := generation.New(generationFixture{}, generation.Config{Workers: 1, QueueSize: 4, MaxJobs: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	server := httpapi.New(engine, httpapi.Options{Generation: manager})
	input := protocol.GenerationRequest{
		ProtocolVersion: protocol.Version, RequestID: "generation.http", Kind: "scene",
		ContextHash: strings.Repeat("a", 64), ResponseFormat: "json_object",
		Messages:    []protocol.GenerationMessage{{Role: "user", Content: "Return JSON."}},
		Temperature: 0.5, MaxTokens: 128,
	}
	response := perform(t, server, "/v1/generation/jobs", input)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit generation: %d %s", response.Code, response.Body.String())
	}
	var submitted struct {
		OK   bool                             `json:"ok"`
		Data protocol.GenerationJobSubmission `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/v1/generation/jobs/"+submitted.Data.JobID, nil)
		response = httptest.NewRecorder()
		server.ServeHTTP(response, request)
		var result struct {
			Data protocol.GenerationJob `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.Status == "succeeded" {
			if result.Data.Result == nil || result.Data.Result.Content != `{"answer":"ok"}` {
				t.Fatalf("unexpected generation: %+v", result.Data)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("structured generation job did not finish")
}

func TestStructuredGenerationUnavailable(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	input := protocol.GenerationRequest{
		ProtocolVersion: protocol.Version, RequestID: "generation.unavailable", Kind: "scene",
		ContextHash: strings.Repeat("a", 64), ResponseFormat: "json_object",
		Messages:  []protocol.GenerationMessage{{Role: "user", Content: "Return JSON."}},
		MaxTokens: 128,
	}
	response := perform(t, server, "/v1/generation/jobs", input)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("generation unavailable: %d %s", response.Code, response.Body.String())
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != "generation_unavailable" {
		t.Fatalf("unexpected generation error: %+v", envelope.Error)
	}
}

type generationFixture struct{}

func (generationFixture) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResponse, error) {
	return provider.CompletionResponse{Content: `{"answer":"ok"}`, Model: "fixture"}, nil
}

var errDefiniteCreateFailure = errors.New("definite create failure")

type definiteCreateFailureStore struct {
	rinruntime.Store
}

func (s *definiteCreateFailureStore) Create(string, protocol.EventRecord) error {
	return errDefiniteCreateFailure
}

func newServer(t *testing.T, options httpapi.Options) http.Handler {
	t.Helper()
	engine, err := rinruntime.Open(store.NewMemory(), policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	return httpapi.New(engine, options)
}

func largeSnapshotCreateRequest(sessionID string) protocol.CreateSessionRequest {
	request := apiCreateRequest()
	request.SessionID = sessionID
	request.RequestID = "create." + sessionID
	request.Actors = make([]protocol.ActorSeed, 128)
	description := strings.Repeat("d", 300)
	motivation := strings.Repeat("m", 300)
	for actorIndex := range request.Actors {
		actorID := fmt.Sprintf("npc.large.%03d", actorIndex)
		goals := make([]protocol.Goal, 32)
		for goalIndex := range goals {
			goals[goalIndex] = protocol.Goal{
				ID:             fmt.Sprintf("goal.large.%03d.%02d", actorIndex, goalIndex),
				Description:    description,
				Motivation:     motivation,
				Priority:       1,
				TargetProgress: 1,
				Status:         "active",
			}
		}
		request.Actors[actorIndex] = protocol.ActorSeed{
			ID:              actorID,
			Kind:            "npc",
			DisplayName:     actorID,
			Goals:           goals,
			ThinkEveryTicks: 1,
			Enabled:         true,
		}
	}
	return request
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

func proposeHTTP(t *testing.T, handler http.Handler, request protocol.ProposeRequest) protocol.ActionProposal {
	t.Helper()
	response := perform(t, handler, "/v1/agent/propose", request)
	if response.Code != http.StatusOK {
		t.Fatalf("propose: %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		OK   bool                    `json:"ok"`
		Data protocol.ProposalResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Data.Proposal.ID == "" {
		t.Fatalf("unexpected proposal response: %+v", envelope)
	}
	return envelope.Data.Proposal
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

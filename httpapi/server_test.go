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
	"github.com/sunrioa/rin/host"
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

	request := jsonRequest(t, "/v2/session/create", apiCreateRequest())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("expected bearer challenge, got %d %s", response.Code, response.Body.String())
	}

	request = jsonRequest(t, "/v2/session/create", apiCreateRequest())
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
	request := httptest.NewRequest(http.MethodPost, "/v2/session/create", strings.NewReader(`{"protocol_version":"rin.protocol/v2","unexpected":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v2/session/get", strings.NewReader(
		`{"protocol_version":"rin.protocol/v2","session_id":"session.first","session_id":"session.last"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":"invalid_json"`) {
		t.Fatalf("duplicate member response: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v2/session/create", strings.NewReader(`{"padding":"`+strings.Repeat("x", 400)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v2/session/create", strings.NewReader(`{}`))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content type status: %d", response.Code)
	}
}

func TestStrictJSONRejectsRawInvalidUTF8BeforeDecoding(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	payload := []byte(`{"protocol_version":"rin.protocol/v2","session_id":"`)
	payload = append(payload, 0xff)
	payload = append(payload, []byte(`"}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v2/session/get", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 status: %d %s", response.Code, response.Body.String())
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != "invalid_json" {
		t.Fatalf("invalid UTF-8 response: %+v", envelope)
	}
	if strings.Contains(response.Body.String(), "\ufffd") {
		t.Fatalf("invalid UTF-8 was replacement-decoded: %s", response.Body.String())
	}
}

func TestProposalWireResponseSeparatesPlayerTextFromPrivateAuditMetadata(t *testing.T) {
	const canary = "PRIVATE_HTTP_BOUNDARY_CANARY_6C2D"
	server := newServer(t, httpapi.Options{})
	create := apiCreateRequest()
	create.Actors[0].Boundaries[0].Description = canary
	if response := perform(t, server, "/v2/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	propose := apiProposeRequest(
		create.SessionID,
		"propose.http.private-boundary",
		create.Actors[0].ID,
		0,
		host.DecisionSequential,
		[]string{create.Actors[0].ID},
		"refuse",
		"rin.dialogue.refuse",
	)
	propose.Tags = []string{"private"}
	propose.Offers[0].Description = "decline the request"
	response := perform(t, server, "/v2/agent/propose", propose)
	if response.Code != http.StatusOK {
		t.Fatalf("propose: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), canary) {
		t.Fatalf("wire response exposed private boundary text: %s", response.Body.String())
	}
	var envelope struct {
		OK   bool                    `json:"ok"`
		Data protocol.ProposalResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	proposal := envelope.Data.Proposal
	if !envelope.OK ||
		proposal.Summary != "Proposes: decline the request" ||
		proposal.Rationale != "Selects a game-authorized refusal." ||
		proposal.BoundaryID != "boundary.private" {
		t.Fatalf("unexpected player/audit wire fields: %+v", proposal)
	}
}

func TestInvalidSnapshotMapsToBadRequest(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	response := perform(t, server, "/v2/session/restore", protocol.RestoreRequest{
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
				Actors: map[string]protocol.ActorState{},
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
	response := perform(t, server, "/v2/session/restore", protocol.RestoreRequest{
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
				Actors:          map[string]protocol.ActorState{},
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
	if response := perform(t, source, "/v2/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("large create: %d %s", response.Code, response.Body.String())
	}
	snapshotResponse := perform(t, source, "/v2/session/snapshot", protocol.SessionRequest{
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
	restoreResponse := perform(t, target, "/v2/session/restore", protocol.RestoreRequest{
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
	response := perform(t, server, "/v2/session/create", apiCreateRequest())
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
	response := perform(t, server, "/v2/session/create", apiCreateRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}

	propose := apiProposeRequest(
		"session.http", "propose.http", "npc.http", 0,
		host.DecisionSequential, []string{"npc.http"}, "talk", "rin.dialogue.say",
	)
	propose.Intent = "Respond without exposing private data."
	propose.Tags = []string{"private"}
	response = perform(t, server, "/v2/agent/propose", propose)
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

func TestActionReportHTTPRecordsOutcomeAfterSessionAdvances(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	create := apiCreateRequest()
	if response := perform(t, server, "/v2/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	proposeResponse := perform(t, server, "/v2/agent/propose", apiProposeRequest(
		"session.http",
		"propose.outcome-report",
		"npc.http",
		0,
		host.DecisionSequential,
		[]string{"npc.http"},
		"wait",
		"rin.world.wait",
	))
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
	if response := perform(t, server, "/v2/session/observe", apiObserveRequest(
		"session.http", "observe.after-apply", "event.after-apply", 5, []string{"npc.http"},
	)); response.Code != http.StatusOK {
		t.Fatalf("observe: %d %s", response.Code, response.Body.String())
	}
	reportResponse := perform(t, server, "/v2/action/report", apiSuccessfulReport(
		proposed.Data.Proposal,
		"report.outcome",
		"event.outcome-report",
		0,
		"The game had already applied this action.",
	))
	if reportResponse.Code != http.StatusOK {
		t.Fatalf("late outcome report: %d %s", reportResponse.Code, reportResponse.Body.String())
	}
	assertResponseOK(t, reportResponse)
}

func TestBatchActionReportHTTPHandlesLateAndMixedBaseOutcomes(t *testing.T) {
	t.Run("late outcome", func(t *testing.T) {
		server := newServer(t, httpapi.Options{})
		create := apiCreateRequest()
		create.Features = []string{protocol.FeatureArbitration}
		if response := perform(t, server, "/v2/session/create", create); response.Code != http.StatusOK {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		proposal := proposeHTTP(t, server, apiProposeRequest(
			create.SessionID,
			"propose.batch-http-late",
			"npc.http",
			0,
			host.DecisionSimultaneous,
			[]string{"npc.http"},
			"wait",
			"rin.world.wait",
		))
		if response := perform(t, server, "/v2/session/observe", apiObserveRequest(
			create.SessionID,
			"observe.batch-http-advance",
			"event.batch-http-advance",
			5,
			[]string{"npc.http"},
		)); response.Code != http.StatusOK {
			t.Fatalf("observe: %d %s", response.Code, response.Body.String())
		}
		report := apiSuccessfulReport(
			proposal,
			"unused",
			"event.batch-http-late",
			0,
			"The game had already applied this batch item.",
		).Report
		response := perform(t, server, "/v2/action/report-batch", protocol.BatchActionReportRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "report.batch-http-late",
			Tick:            0,
			Reports:         []protocol.ActionReport{report},
		})
		if response.Code != http.StatusOK {
			t.Fatalf("late batch outcome: %d %s", response.Code, response.Body.String())
		}
		assertResponseOK(t, response)

		stateResponse := perform(t, server, "/v2/session/get", protocol.SessionRequest{
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
		create.Features = []string{protocol.FeatureArbitration}
		other := create.Actors[0]
		other.ID = "npc.other"
		other.DisplayName = "Other HTTP NPC"
		create.Actors = append(create.Actors, other)
		if response := perform(t, server, "/v2/session/create", create); response.Code != http.StatusOK {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		older := proposeHTTP(t, server, apiProposeRequest(
			create.SessionID,
			"propose.http-base-one",
			"npc.http",
			0,
			host.DecisionSimultaneous,
			[]string{"npc.http", "npc.other"},
			"wait",
			"rin.world.wait",
		))
		if response := perform(t, server, "/v2/session/observe", apiObserveRequest(
			create.SessionID,
			"observe.http-new-base",
			"event.http-new-base",
			5,
			[]string{"npc.http", "npc.other"},
		)); response.Code != http.StatusOK {
			t.Fatalf("observe: %d %s", response.Code, response.Body.String())
		}
		newer := proposeHTTP(t, server, apiProposeRequest(
			create.SessionID,
			"propose.http-base-two",
			"npc.other",
			5,
			host.DecisionSimultaneous,
			[]string{"npc.http", "npc.other"},
			"wait",
			"rin.world.wait",
		))
		olderReport := apiSuccessfulReport(older, "unused", "event.http-old-base", 5, "Old base.").Report
		newerReport := apiSuccessfulReport(newer, "unused", "event.http-new-base-outcome", 5, "New base.").Report
		response := perform(t, server, "/v2/action/report-batch", protocol.BatchActionReportRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			RequestID:       "report.http-mixed-base",
			Tick:            5,
			Reports:         []protocol.ActionReport{olderReport, newerReport},
		})
		if response.Code != http.StatusConflict {
			t.Fatalf("mixed-base batch: %d %s", response.Code, response.Body.String())
		}
		var envelope protocol.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil || envelope.Error.Code != "batch_context_mismatch" {
			t.Fatalf("unexpected mixed-base error: %+v", envelope.Error)
		}
	})
}

func TestTimelineAndReplayHTTPFlow(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	if response := perform(t, server, "/v2/session/create", apiCreateRequest()); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	timelineResponse := perform(t, server, "/v2/session/timeline", protocol.TimelineRequest{
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
	replayResponse := perform(t, server, "/v2/session/replay", protocol.ReplayRequest{
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
	response := perform(t, server, "/v2/session/create", apiCreateRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	input := apiProposeRequest(
		"session.http", "job.http", "npc.http", 0,
		host.DecisionSequential, []string{"npc.http"}, "talk", "rin.dialogue.say",
	)
	response = perform(t, server, "/v2/jobs/propose", input)
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
		request := httptest.NewRequest(http.MethodGet, "/v2/jobs/"+submitted.Data.JobID, nil)
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
			if result.Data.Proposal == nil || result.Data.Proposal.Action.OfferID != "talk" {
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
	response := perform(t, server, "/v2/generation/jobs", input)
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
		request := httptest.NewRequest(http.MethodGet, "/v2/generation/jobs/"+submitted.Data.JobID, nil)
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
	response := perform(t, server, "/v2/generation/jobs", input)
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
	response := perform(t, handler, "/v2/agent/propose", request)
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
		Features:        protocol.RecommendedFeatures(),
		Actors: []protocol.ActorSeed{{
			ID: "npc.http", Kind: "npc", DisplayName: "HTTP NPC", ThinkEveryTicks: 1, Enabled: true,
			Boundaries: []protocol.Boundary{{ID: "boundary.private", Description: "Keep private data private.", TriggerTags: []string{"private"}, Response: "refuse"}},
			Goals:      []protocol.Goal{{ID: "goal.http", Description: "Respond", Priority: 1, TargetProgress: 1, Status: "active"}},
		}},
	}
}

func apiProposeRequest(
	sessionID, requestID, actorID string,
	tick int64,
	mode host.DecisionMode,
	actorIDs []string,
	offerID, capabilityID string,
) protocol.ProposeRequest {
	epoch := protocol.Epoch{
		SessionID: sessionID,
		WorldID:   "world.http",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
	window := protocol.DecisionWindow{
		ID:             fmt.Sprintf("window.%s.%d", requestID, tick),
		Mode:           mode,
		Epoch:          epoch,
		ObservationSeq: uint64(tick) + 1,
		OpenedAt:       protocol.Timepoint{Clock: host.ClockStep, Value: tick},
		Deadline:       protocol.Timepoint{Clock: host.ClockStep, Value: tick + 100},
		ActorIDs:       append([]string(nil), actorIDs...),
	}
	return protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		ActorID:         actorID,
		Tick:            tick,
		Intent:          "Choose one host action.",
		DecisionWindow:  window,
		Offers: []protocol.ActionOffer{{
			OfferID:          offerID,
			DecisionWindowID: window.ID,
			ActorID:          actorID,
			Capability:       protocol.CapabilityRef{ID: capabilityID, Version: "1.0.0"},
			DescriptorDigest: strings.Repeat("a", 64),
			Description:      "Host-authored action.",
			Arguments:        json.RawMessage(`{}`),
			ExpectedEpoch:    epoch,
			ObservationSeq:   window.ObservationSeq,
			Deadline:         window.Deadline,
		}},
	}
}

func apiObserveRequest(
	sessionID, requestID, eventID string,
	tick int64,
	observerIDs []string,
) protocol.ObserveRequest {
	return protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		EventID:         eventID,
		Tick:            tick,
		ObserverIDs:     append([]string(nil), observerIDs...),
		Source:          "game",
		Kind:            "world",
		Summary:         "The authoritative game state advanced.",
		Importance:      1,
		Epoch: protocol.Epoch{
			SessionID: sessionID,
			WorldID:   "world.http",
			Host:      1,
			World:     1,
			Timeline:  1,
		},
		ObservationSeq: uint64(tick) + 1,
	}
}

func apiSuccessfulReport(
	proposal protocol.ActionProposal,
	requestID, eventID string,
	tick int64,
	summary string,
) protocol.ReportActionRequest {
	invocation := protocol.ActionInvocation{
		OperationID:      "operation." + eventID,
		OfferID:          proposal.Action.OfferID,
		DecisionWindowID: proposal.Action.DecisionWindowID,
		ActorID:          proposal.Action.ActorID,
		Capability:       proposal.Action.Capability,
		DescriptorDigest: proposal.Action.DescriptorDigest,
		Arguments:        append(json.RawMessage(nil), proposal.Action.Arguments...),
		Targets:          append([]protocol.HostRef(nil), proposal.Action.Targets...),
		ExpectedEpoch:    proposal.Action.ExpectedEpoch,
		ObservationSeq:   proposal.Action.ObservationSeq,
		Deadline:         proposal.Action.Deadline,
	}
	run := protocol.ActionRun{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		ProgressSeq: 1,
		Progress:    100,
		UpdatedAt:   protocol.Timepoint{Clock: invocation.Deadline.Clock, Value: tick},
	}
	outcome := protocol.ActionOutcome{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     summary,
		Epoch:       invocation.ExpectedEpoch,
		WorldSeq:    1,
		OccurredAt:  protocol.Timepoint{Clock: invocation.Deadline.Clock, Value: tick},
	}
	return protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       proposal.SessionID,
		RequestID:       requestID,
		Tick:            tick,
		Report: protocol.ActionReport{
			ProposalID: proposal.ID,
			EventID:    eventID,
			Decision:   protocol.ActionAccepted,
			Invocation: &invocation,
			Run:        &run,
			Outcome:    &outcome,
			Summary:    summary,
		},
	}
}

package compat_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestReleaseGateFromEmptyDirectories(t *testing.T) {
	sourceDirectory := filepath.Join(t.TempDir(), "source")
	sourceStore, err := store.OpenFile(sourceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	sourceEngine, err := rinruntime.Open(sourceStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	sourceServer := httptest.NewServer(httpapi.New(sourceEngine, httpapi.Options{}))

	binding := protocol.Binding{
		GameID:         "game.release-gate",
		ContentID:      "base",
		ContentVersion: protocol.ContractReleaseVersion,
		ContentHash:    "sha256.release-gate",
	}
	createRequest := protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.release-gate",
		SessionID:       "session.release-gate",
		Binding:         binding,
		Seed:            606,
		Features: []string{
			protocol.FeatureMemoryArchive,
			protocol.FeatureOutcomeReporting,
		},
		Actors: []protocol.ActorSeed{{
			ID:              "actor.mira",
			Kind:            "npc",
			DisplayName:     "Mira",
			ThinkEveryTicks: 1,
			Enabled:         true,
		}},
	}
	created := releaseGatePost[protocol.MutationResult](
		t, sourceServer.URL, "/v1/session/create", createRequest, http.StatusOK,
	)
	if created.Duplicate || created.Revision != 1 {
		t.Fatalf("unexpected create result: %+v", created)
	}
	duplicateCreate := releaseGatePost[protocol.MutationResult](
		t, sourceServer.URL, "/v1/session/create", createRequest, http.StatusOK,
	)
	if !duplicateCreate.Duplicate || duplicateCreate.Revision != created.Revision {
		t.Fatalf("exact create retry was not stable: %+v", duplicateCreate)
	}

	var firstObservation protocol.ObserveRequest
	for tick := int64(1); tick <= 140; tick++ {
		observation := protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       createRequest.SessionID,
			RequestID:       fmt.Sprintf("observe.release.%03d", tick),
			EventID:         fmt.Sprintf("event.release.%03d", tick),
			Tick:            tick,
			ObserverIDs:     []string{"actor.mira"},
			Source:          "release-gate",
			Kind:            "world.event",
			Summary:         fmt.Sprintf("Observed release-gate event %03d.", tick),
			Tags:            []string{"release"},
			Importance:      int(tick%5) + 1,
		}
		if tick == 1 {
			firstObservation = observation
		}
		releaseGatePost[protocol.MutationResult](
			t, sourceServer.URL, "/v1/session/observe", observation, http.StatusOK,
		)
	}
	duplicateObservation := releaseGatePost[protocol.MutationResult](
		t, sourceServer.URL, "/v1/session/observe", firstObservation, http.StatusOK,
	)
	if !duplicateObservation.Duplicate {
		t.Fatalf("exact observation retry was not reported as duplicate: %+v", duplicateObservation)
	}
	conflictingObservation := firstObservation
	conflictingObservation.Summary = "A different payload reused the request identity."
	releaseGateError(
		t,
		sourceServer.URL,
		"/v1/session/observe",
		conflictingObservation,
		http.StatusConflict,
		"request_id_conflict",
	)

	proposal := releaseGatePost[protocol.ProposalResult](
		t,
		sourceServer.URL,
		"/v1/agent/propose",
		protocol.ProposeRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       createRequest.SessionID,
			RequestID:       "propose.release-gate",
			ActorID:         "actor.mira",
			Tick:            140,
			Intent:          "Choose a safe authored action.",
			CandidateActions: []protocol.ActionSpec{{
				ID:          "wait",
				Kind:        "wait",
				Description: "wait and observe",
			}},
		},
		http.StatusOK,
	)
	if proposal.Proposal.Action.ID != "wait" || proposal.Proposal.Status != "pending" {
		t.Fatalf("unexpected proposal: %+v", proposal)
	}
	commitRequest := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       createRequest.SessionID,
		RequestID:       "commit.release-gate",
		ProposalID:      proposal.Proposal.ID,
		EventID:         "event.commit.release-gate",
		Tick:            141,
		Accepted:        true,
		Outcome:         "The game applied the authored wait action.",
	}
	committed := releaseGatePost[protocol.MutationResult](
		t, sourceServer.URL, "/v1/action/commit", commitRequest, http.StatusOK,
	)
	duplicateCommit := releaseGatePost[protocol.MutationResult](
		t, sourceServer.URL, "/v1/action/commit", commitRequest, http.StatusOK,
	)
	if duplicateCommit.Revision != committed.Revision || !duplicateCommit.Duplicate {
		t.Fatalf("exact commit retry was not stable: first=%+v duplicate=%+v", committed, duplicateCommit)
	}

	state := releaseGatePost[protocol.SessionState](
		t,
		sourceServer.URL,
		"/v1/session/get",
		protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: createRequest.SessionID},
		http.StatusOK,
	)
	actor := state.Actors["actor.mira"]
	if len(actor.MemorySummaries) == 0 || state.Proposals[proposal.Proposal.ID].Status != "accepted" {
		t.Fatalf("long-session state was not closed: summaries=%d proposal=%+v", len(actor.MemorySummaries), state.Proposals[proposal.Proposal.ID])
	}
	replayed := releaseGatePost[protocol.Snapshot](
		t,
		sourceServer.URL,
		"/v1/session/replay",
		protocol.ReplayRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       createRequest.SessionID,
			Revision:        state.Revision,
		},
		http.StatusOK,
	)
	snapshot := releaseGatePost[protocol.Snapshot](
		t,
		sourceServer.URL,
		"/v1/session/snapshot",
		protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: createRequest.SessionID},
		http.StatusOK,
	)
	if replayed.State.HeadHash != snapshot.State.HeadHash || replayed.State.Revision != snapshot.State.Revision {
		t.Fatalf("replay and snapshot diverged: replay=%d/%s snapshot=%d/%s",
			replayed.State.Revision,
			replayed.State.HeadHash,
			snapshot.State.Revision,
			snapshot.State.HeadHash,
		)
	}

	sourceServer.Close()
	if err := sourceStore.Close(); err != nil {
		t.Fatal(err)
	}

	targetDirectory := filepath.Join(t.TempDir(), "target")
	targetStore, err := store.OpenFile(targetDirectory)
	if err != nil {
		t.Fatal(err)
	}
	targetEngine, err := rinruntime.Open(targetStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	targetServer := httptest.NewServer(httpapi.New(targetEngine, httpapi.Options{}))
	restoreRequest := protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       createRequest.SessionID,
		RequestID:       "restore.release-gate",
		ExpectedBinding: binding,
		Snapshot:        snapshot,
	}
	wrongBinding := restoreRequest
	wrongBinding.RequestID = "restore.release-gate.wrong-binding"
	wrongBinding.ExpectedBinding.ContentHash = "sha256.wrong-content"
	releaseGateError(
		t,
		targetServer.URL,
		"/v1/session/restore",
		wrongBinding,
		http.StatusConflict,
		"binding_mismatch",
	)
	restored := releaseGatePost[protocol.MutationResult](
		t, targetServer.URL, "/v1/session/restore", restoreRequest, http.StatusOK,
	)
	if restored.Duplicate || restored.Revision != 1 {
		t.Fatalf("fresh restore did not create one durable target event: %+v", restored)
	}
	restoredState := releaseGatePost[protocol.SessionState](
		t,
		targetServer.URL,
		"/v1/session/get",
		protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: createRequest.SessionID},
		http.StatusOK,
	)
	restoredProposalStatus := ""
	for _, recent := range restoredState.Actors["actor.mira"].RecentActions {
		if recent.ID == proposal.Proposal.ID {
			restoredProposalStatus = recent.Status
			break
		}
	}
	if restoredState.Tick != state.Tick ||
		restoredProposalStatus != "accepted" ||
		len(restoredState.Actors["actor.mira"].MemorySummaries) == 0 {
		t.Fatalf(
			"restored state lost long-session data: tick=%d want=%d proposal_status=%q summaries=%d",
			restoredState.Tick,
			state.Tick,
			restoredProposalStatus,
			len(restoredState.Actors["actor.mira"].MemorySummaries),
		)
	}

	targetServer.Close()
	if err := targetStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := store.OpenFile(targetDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopenedEngine, err := rinruntime.Open(reopenedStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopenedEngine.VerifyAll(); err != nil {
		t.Fatalf("restored target failed full genesis verification: %v", err)
	}
	reopenedState, err := reopenedEngine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       createRequest.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopenedState.HeadHash != restoredState.HeadHash || reopenedState.Tick != restoredState.Tick {
		t.Fatalf("restart changed restored state: before=%d/%s after=%d/%s",
			restoredState.Tick,
			restoredState.HeadHash,
			reopenedState.Tick,
			reopenedState.HeadHash,
		)
	}
}

func releaseGatePost[T any](
	t *testing.T,
	baseURL string,
	path string,
	request any,
	wantStatus int,
) T {
	t.Helper()
	response := releaseGateRequest(t, baseURL, path, request)
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s status=%d want=%d body=%s", path, response.StatusCode, wantStatus, payload)
	}
	var envelope struct {
		OK    bool                  `json:"ok"`
		Data  json.RawMessage       `json:"data"`
		Error *protocol.ErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("%s decode envelope: %v body=%s", path, err, payload)
	}
	if !envelope.OK || envelope.Error != nil {
		t.Fatalf("%s returned failure envelope: %s", path, payload)
	}
	var result T
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		t.Fatalf("%s decode data: %v data=%s", path, err, envelope.Data)
	}
	return result
}

func releaseGateError(
	t *testing.T,
	baseURL string,
	path string,
	request any,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	response := releaseGateRequest(t, baseURL, path, request)
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("%s decode error envelope: %v body=%s", path, err, payload)
	}
	if response.StatusCode != wantStatus ||
		envelope.OK ||
		envelope.Error == nil ||
		envelope.Error.Code != wantCode {
		t.Fatalf("%s error=%d/%+v want=%d/%s body=%s",
			path,
			response.StatusCode,
			envelope.Error,
			wantStatus,
			wantCode,
			payload,
		)
	}
}

func releaseGateRequest(t *testing.T, baseURL string, path string, request any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

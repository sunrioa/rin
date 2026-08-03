package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
)

const testControlToken = "0123456789abcdef0123456789abcdef"

func TestHTTPHandlerRegistersAndPublishes(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{5}, 64)),
	})
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: testControlToken})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}

	lease := HostLease{}
	response := requestJSON(t, handler, "/control/v1/register", registration("instance.one"))
	if response.Code != http.StatusOK {
		t.Fatalf("register status = %d, body = %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil {
		t.Fatalf("decode lease: %v", err)
	}

	response = requestJSON(t, handler, "/control/v1/publish", publishRequest{
		HostID:      "test.host",
		LeaseID:     lease.LeaseID,
		Publication: worldPublication(1, "ready"),
	})
	if response.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", response.Code, response.Body)
	}
	actors, err := service.ListActors(
		readPrincipal(), "test.host", "world.one",
	)
	if err != nil || len(actors) != 1 {
		t.Fatalf("ListActors = %#v, %v", actors, err)
	}
}

func TestHTTPHandlerRequiresTokenAndStrictJSON(t *testing.T) {
	service := New(Options{})
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: testControlToken})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}

	unauthorized := httptest.NewRequest(
		http.MethodPost, "/control/v1/register",
		bytes.NewReader([]byte(`{}`)),
	)
	unauthorized.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.Code)
	}

	unknown := httptest.NewRequest(
		http.MethodPost, "/control/v1/register",
		bytes.NewReader([]byte(`{"unknown":true}`)),
	)
	unknown.Header.Set("Authorization", "Bearer "+testControlToken)
	unknown.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, unknown)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d, body = %s", response.Code, response.Body)
	}

	duplicate := httptest.NewRequest(
		http.MethodPost, "/control/v1/register",
		bytes.NewReader([]byte(`{"host_id":"one","host_id":"two"}`)),
	)
	duplicate.Header.Set("Authorization", "Bearer "+testControlToken)
	duplicate.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, duplicate)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate-field status = %d, body = %s", response.Code, response.Body)
	}

	clientInfo := httptest.NewRequest(
		http.MethodGet,
		"/control/v1/client/info",
		nil,
	)
	clientInfo.Header.Set("Authorization", "Bearer "+testControlToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, clientInfo)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured client API status = %d", response.Code)
	}
}

func TestHTTPHandlerDeliversAndRecordsOperationLifecycle(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(sequenceBytes(256)),
	})
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: testControlToken})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}

	lease := HostLease{}
	response := requestJSON(
		t,
		handler,
		"/control/v1/register",
		registration("instance.operation.http"),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("register status = %d, body = %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil {
		t.Fatalf("decode lease: %v", err)
	}
	response = requestJSON(t, handler, "/control/v1/publish", publishRequest{
		HostID:      "test.host",
		LeaseID:     lease.LeaseID,
		Publication: worldPublication(1, "ready"),
	})
	if response.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", response.Code, response.Body)
	}

	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.http.message",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Hello over Host Control.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	response = requestJSON(t, handler, "/control/v1/poll", pollRequest{
		HostID:     "test.host",
		LeaseID:    lease.LeaseID,
		Limit:      8,
		WaitMillis: 0,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("poll status = %d, body = %s", response.Code, response.Body)
	}
	var batch HostControlBatch
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != operation.OperationID {
		t.Fatalf("poll batch = %#v", batch)
	}
	response = requestJSON(t, handler, "/control/v1/outcome", outcomeRequest{
		HostID:  "test.host",
		LeaseID: lease.LeaseID,
		Outcome: host.ActionOutcome{
			OperationID: operation.OperationID,
			Status:      host.ActionStale,
			Code:        "host.not_started",
			Summary:     "Execution did not start.",
			Epoch:       testEpoch(),
			WorldSeq:    1,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 1},
		},
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("unaccepted outcome status = %d, body = %s", response.Code, response.Body)
	}
	var notAccepted errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &notAccepted); err != nil {
		t.Fatalf("decode unaccepted outcome error: %v", err)
	}
	if notAccepted.Code != "not_accepted" {
		t.Fatalf("unaccepted outcome error = %#v", notAccepted)
	}

	response = requestJSON(
		t,
		handler,
		"/control/v1/ack",
		acknowledgementRequest{
			HostID:  "test.host",
			LeaseID: lease.LeaseID,
			Acknowledgement: HostAcknowledgement{
				OperationID: operation.OperationID,
				Accepted:    true,
			},
		},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("ack status = %d, body = %s", response.Code, response.Body)
	}
	response = requestJSON(t, handler, "/control/v1/run", runRequest{
		HostID:  "test.host",
		LeaseID: lease.LeaseID,
		Run: host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionRunning,
			ProgressSeq: 1,
			Progress:    50,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 2},
		},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", response.Code, response.Body)
	}
	response = requestJSON(t, handler, "/control/v1/outcome", outcomeRequest{
		HostID:  "test.host",
		LeaseID: lease.LeaseID,
		Outcome: host.ActionOutcome{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			Summary:     "The actor replied.",
			Epoch:       testEpoch(),
			WorldSeq:    2,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 3},
		},
		Output: json.RawMessage(
			`{"type":"actor_turn","reply":"Ready.","capability":"activity.wait"}`,
		),
	})
	if response.Code != http.StatusOK {
		t.Fatalf("outcome status = %d, body = %s", response.Code, response.Body)
	}
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil ||
		view.Status != OperationSucceeded ||
		view.Output["reply"] != "Ready." ||
		view.Output["capability"] != "activity.wait" {
		t.Fatalf("GetOperation = %#v, %v", view, err)
	}
}

func TestHTTPHandlerReturnsStableNotFoundCodeForMissingOutcome(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{9}, 64)),
	})
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: testControlToken})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	lease := HostLease{}
	response := requestJSON(
		t,
		handler,
		"/control/v1/register",
		registration("instance.missing.outcome"),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("register status = %d, body = %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil {
		t.Fatalf("decode lease: %v", err)
	}
	response = requestJSON(t, handler, "/control/v1/outcome", outcomeRequest{
		HostID:  "test.host",
		LeaseID: "lease.expired",
		Outcome: host.ActionOutcome{
			OperationID: "operation.pending",
			Status:      host.ActionFailed,
			Code:        "host.failed",
			Summary:     "The lease must be renewed before reporting.",
			Epoch:       testEpoch(),
			WorldSeq:    2,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 3},
		},
	})
	if response.Code != http.StatusGone {
		t.Fatalf("expired lease status = %d, body = %s", response.Code, response.Body)
	}
	var expired errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &expired); err != nil {
		t.Fatalf("decode expired lease error: %v", err)
	}
	if expired.Code != "lease_expired" {
		t.Fatalf("expired lease error = %#v", expired)
	}
	response = requestJSON(t, handler, "/control/v1/outcome", outcomeRequest{
		HostID:  "test.host",
		LeaseID: lease.LeaseID,
		Outcome: host.ActionOutcome{
			OperationID: "operation.missing",
			Status:      host.ActionFailed,
			Code:        "host.failed",
			Summary:     "The operation is no longer present.",
			Epoch:       testEpoch(),
			WorldSeq:    2,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 3},
		},
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing outcome status = %d, body = %s", response.Code, response.Body)
	}
	var remote errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &remote); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if remote.Code != "not_found" || remote.Error == "" {
		t.Fatalf("missing outcome error = %#v", remote)
	}
}

func requestJSON(
	t *testing.T,
	handler http.Handler,
	path string,
	value any,
) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func readPrincipal() host.Principal {
	return host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{ScopeActorRead},
	}
}

func sequenceBytes(size int) []byte {
	value := make([]byte, size)
	for index := range value {
		value[index] = byte(index)
	}
	return value
}

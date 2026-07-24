package httpapi_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestCommitAcceptedPresenceIsRequiredAndBooleanValuesRemainValid(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	commitBase := `{
		"protocol_version":"rin.protocol/v1",
		"session_id":"session.missing",
		"request_id":"request.commit.presence",
		"proposal_id":"proposal.missing",
		"event_id":"event.commit.presence",
		"tick":0%s
	}`
	batchBase := `{
		"protocol_version":"rin.protocol/v1",
		"session_id":"session.missing",
		"request_id":"request.batch.presence",
		"tick":0,
		"items":[{
			"proposal_id":"proposal.missing",
			"event_id":"event.batch.presence"%s
		}]
	}`
	tests := []struct {
		name       string
		path       string
		payload    string
		wantStatus int
		wantField  string
	}{
		{"commit missing", "/v1/action/commit", fmt.Sprintf(commitBase, ""), http.StatusBadRequest, "accepted"},
		{"commit null", "/v1/action/commit", fmt.Sprintf(commitBase, `,"accepted":null`), http.StatusBadRequest, "accepted"},
		{"commit false", "/v1/action/commit", fmt.Sprintf(commitBase, `,"accepted":false`), http.StatusNotFound, ""},
		{"commit true", "/v1/action/commit", fmt.Sprintf(commitBase, `,"accepted":true,"outcome":"shown"`), http.StatusNotFound, ""},
		{"batch missing", "/v1/action/commit-batch", fmt.Sprintf(batchBase, ""), http.StatusBadRequest, "items[0].accepted"},
		{"batch null", "/v1/action/commit-batch", fmt.Sprintf(batchBase, `,"accepted":null`), http.StatusBadRequest, "items[0].accepted"},
		{"batch false", "/v1/action/commit-batch", fmt.Sprintf(batchBase, `,"accepted":false`), http.StatusNotFound, ""},
		{"batch true", "/v1/action/commit-batch", fmt.Sprintf(batchBase, `,"accepted":true,"outcome":"shown"`), http.StatusNotFound, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRawJSON(server, http.MethodPost, test.path, test.payload)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			var envelope protocol.APIResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if test.wantField != "" {
				if envelope.Error == nil ||
					envelope.Error.Code != "invalid_request" ||
					envelope.Error.Field != test.wantField {
					t.Fatalf("error=%+v, want invalid_request field %s", envelope.Error, test.wantField)
				}
			} else if envelope.Error == nil || envelope.Error.Code != "session_not_found" {
				t.Fatalf("explicit boolean did not reach runtime validation: %+v", envelope.Error)
			}
		})
	}
}

func TestOpenAPIRequiredLegalZeroValuesReachHandlers(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	payload := `{
		"protocol_version":"rin.protocol/v1",
		"request_id":"request.create.zero-values",
		"session_id":"session.zero-values",
		"binding":{
			"game_id":"game.example",
			"content_id":"base",
			"content_version":"1",
			"content_hash":"hash"
		},
		"seed":0,
		"actors":[{
			"id":"actor.zero",
			"kind":"npc",
			"display_name":"Zero",
			"boundaries":[{
				"id":"boundary.zero",
				"description":"A boundary with no trigger tags.",
				"trigger_tags":null,
				"response":"wait"
			}],
			"think_every_ticks":1,
			"enabled":false
		}]
	}`
	response := performRawJSON(server, http.MethodPost, "/v1/session/create", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("legal zero values were rejected: %d %s", response.Code, response.Body.String())
	}
	var omittedDefaults map[string]any
	if err := json.Unmarshal([]byte(payload), &omittedDefaults); err != nil {
		t.Fatal(err)
	}
	omittedDefaults["request_id"] = "request.create.omitted-defaults"
	omittedDefaults["session_id"] = "session.omitted-defaults"
	delete(omittedDefaults, "seed")
	actor := omittedDefaults["actors"].([]any)[0].(map[string]any)
	delete(actor, "enabled")
	delete(actor["boundaries"].([]any)[0].(map[string]any), "trigger_tags")
	omittedPayload, err := json.Marshal(omittedDefaults)
	if err != nil {
		t.Fatal(err)
	}
	omittedResponse := performRawJSON(server, http.MethodPost, "/v1/session/create", string(omittedPayload))
	if omittedResponse.Code != http.StatusOK {
		t.Fatalf("compatible zero-value omissions were rejected: %d %s", omittedResponse.Code, omittedResponse.Body.String())
	}
	stateResponse := perform(t, server, "/v1/session/get", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.zero-values",
	})
	if stateResponse.Code != http.StatusOK {
		t.Fatalf("state status=%d body=%s", stateResponse.Code, stateResponse.Body.String())
	}
	var stateEnvelope struct {
		Data struct {
			Actors map[string]struct {
				Boundaries []struct {
					TriggerTags any `json:"trigger_tags"`
				} `json:"boundaries"`
			} `json:"actors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &stateEnvelope); err != nil {
		t.Fatal(err)
	}
	boundaries := stateEnvelope.Data.Actors["actor.zero"].Boundaries
	if len(boundaries) != 1 || boundaries[0].TriggerTags != nil {
		t.Fatalf("nil trigger_tags did not round-trip as schema-authorized null: %+v", boundaries)
	}
	for _, emptyCollection := range []struct {
		path  string
		value any
		field string
	}{
		{
			"/v1/session/timeline",
			protocol.TimelineRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.zero-values",
				AfterRevision:   1,
				Limit:           1,
			},
			"entries",
		},
		{
			"/v1/scheduler/due",
			protocol.DueAgentsRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.zero-values",
				Tick:            0,
				Limit:           1,
			},
			"agents",
		},
	} {
		collectionResponse := perform(t, server, emptyCollection.path, emptyCollection.value)
		if collectionResponse.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", emptyCollection.path, collectionResponse.Code, collectionResponse.Body.String())
		}
		var collectionEnvelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(collectionResponse.Body.Bytes(), &collectionEnvelope); err != nil {
			t.Fatal(err)
		}
		values, ok := collectionEnvelope.Data[emptyCollection.field].([]any)
		if !ok || len(values) != 0 {
			t.Fatalf("%s.%s must encode as [], got %#v", emptyCollection.path, emptyCollection.field, collectionEnvelope.Data[emptyCollection.field])
		}
	}
}

func TestJobPathIdentifiersAreValidatedBeforeManagerAvailability(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/jobs/bad!"},
		{http.MethodDelete, "/v1/jobs/bad!"},
		{http.MethodGet, "/v1/generation/jobs/bad!"},
		{http.MethodDelete, "/v1/generation/jobs/bad!"},
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
			continue
		}
		var envelope protocol.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil ||
			envelope.Error.Code != "invalid_request" ||
			envelope.Error.Field != "job_id" {
			t.Errorf("%s %s error=%+v", test.method, test.path, envelope.Error)
		}
	}
}

func TestHTTPUnknownFieldsFollowContractAndSnapshotHash(t *testing.T) {
	assertUnknown := func(
		t *testing.T,
		handler http.Handler,
		path string,
		payload map[string]any,
		field string,
	) {
		t.Helper()
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response := performRawJSON(handler, http.MethodPost, path, string(encoded))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var envelope protocol.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil ||
			envelope.Error.Code != "invalid_json" ||
			envelope.Error.Field != field {
			t.Fatalf("error=%+v, want invalid_json field %s", envelope.Error, field)
		}
	}

	closedServer := newServer(t, httpapi.Options{})
	for _, test := range []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{
			"request root",
			"future",
			func(value map[string]any) { value["future"] = true },
		},
		{
			"request binding",
			"binding.future",
			func(value map[string]any) {
				value["binding"].(map[string]any)["future"] = true
			},
		},
		{
			"nested actor input",
			"actors[0].future",
			func(value map[string]any) {
				value["actors"].([]any)[0].(map[string]any)["future"] = true
			},
		},
	} {
		t.Run("closed "+test.name, func(t *testing.T) {
			payload := toJSONMap(t, apiCreateRequest())
			test.mutate(payload)
			assertUnknown(t, closedServer, "/v1/session/create", payload, test.field)
		})
	}

	source := newServer(t, httpapi.Options{})
	create := apiCreateRequest()
	create.SessionID = "session.additive-snapshot"
	create.RequestID = "request.create.additive-snapshot"
	if response := perform(t, source, "/v1/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	snapshotResponse := perform(t, source, "/v1/session/snapshot", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	})
	if snapshotResponse.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", snapshotResponse.Code, snapshotResponse.Body.String())
	}
	var snapshotEnvelope struct {
		Data protocol.Snapshot `json:"data"`
	}
	if err := json.Unmarshal(snapshotResponse.Body.Bytes(), &snapshotEnvelope); err != nil {
		t.Fatal(err)
	}
	restore := protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "request.restore.additive-snapshot",
		ExpectedBinding: create.Binding,
		Snapshot:        snapshotEnvelope.Data,
	}
	restorePayload := toJSONMap(t, restore)
	missingKnownPayload := toJSONMap(t, restorePayload)
	missingActor := missingKnownPayload["snapshot"].(map[string]any)["state"].(map[string]any)["actors"].(map[string]any)["npc.http"].(map[string]any)
	delete(missingActor, "enabled")
	missingKnownEncoded, err := json.Marshal(missingKnownPayload)
	if err != nil {
		t.Fatal(err)
	}
	missingKnownResponse := performRawJSON(
		newServer(t, httpapi.Options{}),
		http.MethodPost,
		"/v1/session/restore",
		string(missingKnownEncoded),
	)
	if missingKnownResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"missing known state field status=%d body=%s",
			missingKnownResponse.Code,
			missingKnownResponse.Body.String(),
		)
	}
	var missingKnownEnvelope protocol.APIResponse
	if err := json.Unmarshal(missingKnownResponse.Body.Bytes(), &missingKnownEnvelope); err != nil {
		t.Fatal(err)
	}
	if missingKnownEnvelope.Error == nil ||
		missingKnownEnvelope.Error.Code != "invalid_request" ||
		missingKnownEnvelope.Error.Field != "snapshot.state.actors.npc.http.enabled" {
		t.Fatalf("missing known state field error=%+v", missingKnownEnvelope.Error)
	}
	for _, test := range []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{
			"restore root",
			"future",
			func(value map[string]any) { value["future"] = true },
		},
		{
			"expected binding",
			"expected_binding.future",
			func(value map[string]any) {
				value["expected_binding"].(map[string]any)["future"] = true
			},
		},
	} {
		t.Run("closed "+test.name, func(t *testing.T) {
			payload := toJSONMap(t, restorePayload)
			test.mutate(payload)
			assertUnknown(
				t,
				newServer(t, httpapi.Options{}),
				"/v1/session/restore",
				payload,
				test.field,
			)
		})
	}

	oversizedPayload := toJSONMap(t, restorePayload)
	oversizedPayload["snapshot"].(map[string]any)["future_blob"] = strings.Repeat(
		"x",
		rinruntime.MaxInlineSnapshotBytes,
	)
	oversizedEncoded, err := json.Marshal(oversizedPayload)
	if err != nil {
		t.Fatal(err)
	}
	oversizedResponse := performRawJSON(
		newServer(t, httpapi.Options{}),
		http.MethodPost,
		"/v1/session/restore",
		string(oversizedEncoded),
	)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"additive snapshot size status=%d body=%s",
			oversizedResponse.Code,
			oversizedResponse.Body.String(),
		)
	}
	var oversizedEnvelope protocol.APIResponse
	if err := json.Unmarshal(oversizedResponse.Body.Bytes(), &oversizedEnvelope); err != nil {
		t.Fatal(err)
	}
	if oversizedEnvelope.Error == nil ||
		oversizedEnvelope.Error.Code != "snapshot_too_large" ||
		oversizedEnvelope.Error.Field != "snapshot" {
		t.Fatalf("additive snapshot size error=%+v", oversizedEnvelope.Error)
	}

	additivePayload := toJSONMap(t, restorePayload)
	additiveSnapshot := additivePayload["snapshot"].(map[string]any)
	additiveState := additiveSnapshot["state"].(map[string]any)
	knownStateHash := hashCanonicalJSON(t, snapshotEnvelope.Data.State)
	if knownStateHash != additiveSnapshot["state_hash"] {
		t.Fatalf(
			"test snapshot hash=%v, canonical known-state hash=%s",
			additiveSnapshot["state_hash"],
			knownStateHash,
		)
	}
	additiveSnapshot["future_snapshot"] = map[string]any{"format": 2}
	additiveState["future_state"] = "ignored by this protocol version"
	additiveState["binding"].(map[string]any)["future_binding"] = true
	additiveState["actors"].(map[string]any)["npc.http"].(map[string]any)["future_actor"] = true
	additiveEncoded, err := json.Marshal(additivePayload)
	if err != nil {
		t.Fatal(err)
	}
	target := newServer(t, httpapi.Options{})
	additiveResponse := performRawJSON(
		target,
		http.MethodPost,
		"/v1/session/restore",
		string(additiveEncoded),
	)
	if additiveResponse.Code != http.StatusOK {
		t.Fatalf(
			"OpenAPI-additive Snapshot/State fields were rejected: %d %s",
			additiveResponse.Code,
			additiveResponse.Body.String(),
		)
	}
	if stateResponse := perform(t, target, "/v1/session/get", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	}); stateResponse.Code != http.StatusOK {
		t.Fatalf("restored state: %d %s", stateResponse.Code, stateResponse.Body.String())
	}

	inclusivePayload := toJSONMap(t, restorePayload)
	inclusivePayload["request_id"] = "request.restore.hash-includes-future"
	inclusiveSnapshot := inclusivePayload["snapshot"].(map[string]any)
	inclusiveState := inclusiveSnapshot["state"].(map[string]any)
	inclusiveState["future_state"] = "included in a future producer hash"
	inclusiveSnapshot["state_hash"] = hashJSONWithAppendedMember(
		t,
		snapshotEnvelope.Data.State,
		`"future_state":"included in a future producer hash"`,
	)
	inclusiveEncoded, err := json.Marshal(inclusivePayload)
	if err != nil {
		t.Fatal(err)
	}
	inclusiveResponse := performRawJSON(
		newServer(t, httpapi.Options{}),
		http.MethodPost,
		"/v1/session/restore",
		string(inclusiveEncoded),
	)
	if inclusiveResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"future-inclusive state hash status=%d body=%s",
			inclusiveResponse.Code,
			inclusiveResponse.Body.String(),
		)
	}
	var inclusiveEnvelope protocol.APIResponse
	if err := json.Unmarshal(inclusiveResponse.Body.Bytes(), &inclusiveEnvelope); err != nil {
		t.Fatal(err)
	}
	if inclusiveEnvelope.Error == nil ||
		inclusiveEnvelope.Error.Code != "invalid_snapshot" ||
		inclusiveEnvelope.Error.Field != "snapshot.state_hash" {
		t.Fatalf("future-inclusive state hash error=%+v", inclusiveEnvelope.Error)
	}
}

func TestHealthPublishesReleaseIdentity(t *testing.T) {
	server := newServer(t, httpapi.Options{})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			ProtocolVersion string `json:"protocol_version"`
			ReleaseVersion  string `json:"release_version"`
			ReleaseStatus   string `json:"release_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK ||
		envelope.Data.ProtocolVersion != protocol.Version ||
		envelope.Data.ReleaseVersion != protocol.ContractReleaseVersion ||
		envelope.Data.ReleaseStatus != protocol.ContractReleaseStatus {
		t.Fatalf("health release identity=%+v", envelope)
	}
}

func toJSONMap(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func hashCanonicalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func hashJSONWithAppendedMember(t *testing.T, value any, member string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) < 2 || encoded[len(encoded)-1] != '}' {
		t.Fatalf("value is not a JSON object: %s", encoded)
	}
	extended := append([]byte(nil), encoded[:len(encoded)-1]...)
	extended = append(extended, ',')
	extended = append(extended, member...)
	extended = append(extended, '}')
	return fmt.Sprintf("%x", sha256.Sum256(extended))
}

func performRawJSON(
	handler http.Handler,
	method string,
	path string,
	payload string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

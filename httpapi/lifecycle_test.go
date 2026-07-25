package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/protocol"
)

func TestSessionLifecycleHTTPIsAuthenticatedAndFailClosed(t *testing.T) {
	t.Parallel()
	server := newServer(t, httpapi.Options{Token: "lifecycle-token"})
	create := `{
		"protocol_version":"rin.protocol/v1",
		"request_id":"create.http.lifecycle",
		"session_id":"session.http.lifecycle",
		"binding":{
			"game_id":"game.lifecycle",
			"content_id":"base",
			"content_version":"1",
			"content_hash":"hash"
		},
		"features":["outcome-reporting-v1"],
		"actors":[{
			"id":"npc.lifecycle",
			"kind":"npc",
			"display_name":"Lifecycle",
			"think_every_ticks":5,
			"enabled":true
		}]
	}`
	createdResponse := performAuthorizedJSON(
		server,
		"/v1/session/create",
		create,
		"lifecycle-token",
	)
	var createdEnvelope struct {
		Data protocol.MutationResult `json:"data"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &createdEnvelope); err != nil {
		t.Fatal(err)
	}
	if createdResponse.Code != http.StatusOK {
		t.Fatalf("create: %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	session := `{"protocol_version":"rin.protocol/v1","session_id":"session.http.lifecycle"}`
	unauthorized := performRawJSON(server, http.MethodPost, "/v1/session/stats", session)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("stats without token = %d", unauthorized.Code)
	}
	stats := performAuthorizedJSON(
		server,
		"/v1/session/stats",
		session,
		"lifecycle-token",
	)
	if stats.Code != http.StatusOK ||
		!strings.Contains(stats.Body.String(), `"lifecycle":"active"`) {
		t.Fatalf("stats: %d %s", stats.Code, stats.Body.String())
	}
	binding := `"expected_binding":{"game_id":"game.lifecycle","content_id":"base",` +
		`"content_version":"1","content_hash":"hash"}`
	archive := `{"protocol_version":"rin.protocol/v1",` +
		`"session_id":"session.http.lifecycle","request_id":"archive.http.lifecycle",` +
		binding + `,"expected_revision":` +
		jsonNumber(createdEnvelope.Data.Revision) + `,"expected_head_hash":"` +
		createdEnvelope.Data.HeadHash + `"}`
	archived := performAuthorizedJSON(
		server,
		"/v1/session/archive",
		archive,
		"lifecycle-token",
	)
	if archived.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", archived.Code, archived.Body.String())
	}
	var archiveEnvelope struct {
		Data protocol.ArchiveSessionResult `json:"data"`
	}
	if err := json.Unmarshal(archived.Body.Bytes(), &archiveEnvelope); err != nil {
		t.Fatal(err)
	}
	observe := `{"protocol_version":"rin.protocol/v1",` +
		`"session_id":"session.http.lifecycle","request_id":"observe.archived",` +
		`"event_id":"event.archived","observer_ids":["npc.lifecycle"],` +
		`"source":"player","kind":"dialogue","summary":"Blocked","importance":1}`
	blocked := performAuthorizedJSON(
		server,
		"/v1/session/observe",
		observe,
		"lifecycle-token",
	)
	if blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), `"code":"session_archived"`) {
		t.Fatalf("archived mutation: %d %s", blocked.Code, blocked.Body.String())
	}
	deletion := `{"protocol_version":"rin.protocol/v1",` +
		`"session_id":"session.http.lifecycle","request_id":"delete.http.lifecycle",` +
		binding + `,"expected_revision":` +
		jsonNumber(createdEnvelope.Data.Revision) + `,"expected_head_hash":"` +
		createdEnvelope.Data.HeadHash + `","archive_receipt_id":"` +
		archiveEnvelope.Data.ReceiptID +
		`","confirmation":"session.http.lifecycle"}`
	deleted := performAuthorizedJSON(
		server,
		"/v1/session/delete",
		deletion,
		"lifecycle-token",
	)
	if deleted.Code != http.StatusOK ||
		!strings.Contains(deleted.Body.String(), `"duplicate":false`) {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	retry := performAuthorizedJSON(
		server,
		"/v1/session/delete",
		deletion,
		"lifecycle-token",
	)
	if retry.Code != http.StatusOK ||
		!strings.Contains(retry.Body.String(), `"duplicate":true`) {
		t.Fatalf("delete retry: %d %s", retry.Code, retry.Body.String())
	}
}

func performAuthorizedJSON(
	server http.Handler,
	path string,
	payload string,
	token string,
) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request, _ := http.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(payload),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	server.ServeHTTP(response, request)
	return response
}

func jsonNumber(value uint64) string {
	return strconv.FormatUint(value, 10)
}

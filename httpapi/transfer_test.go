package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestHTTPTransferRoundTrip(t *testing.T) {
	source := transferHTTPServer(t)
	create := apiCreateRequest()
	create.SessionID = "session.http-transfer"
	create.RequestID = "create.http-transfer"
	if response := perform(t, source, "/v1/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}

	export := perform(t, source, "/v1/session/export", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	})
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	if contentType := export.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/x-ndjson") {
		t.Fatalf("export content type = %q", contentType)
	}
	lines := bytes.Split(bytes.TrimSuffix(export.Body.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("export frame count = %d, want manifest/event/complete", len(lines))
	}
	for index, expected := range []string{"manifest", "event", "complete"} {
		var discriminator struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(lines[index], &discriminator); err != nil {
			t.Fatal(err)
		}
		if discriminator.Type != expected {
			t.Fatalf("frame %d type = %q, want %q", index, discriminator.Type, expected)
		}
	}

	target := transferHTTPServer(t)
	importResponse := importTransfer(t, target, export.Body.Bytes(), create.Binding)
	if importResponse.Code != http.StatusOK {
		t.Fatalf("import: %d %s", importResponse.Code, importResponse.Body.String())
	}
	state := perform(t, target, "/v1/session/get", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	})
	if state.Code != http.StatusOK {
		t.Fatalf("imported state: %d %s", state.Code, state.Body.String())
	}
}

func TestHTTPTransferFailsClosedForCorruptionAndFraming(t *testing.T) {
	source := transferHTTPServer(t)
	create := apiCreateRequest()
	create.SessionID = "session.http-transfer-invalid"
	create.RequestID = "create.http-transfer-invalid"
	if response := perform(t, source, "/v1/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	export := perform(t, source, "/v1/session/export", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	})
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	corruptedLines := bytes.Split(
		bytes.TrimSuffix(export.Body.Bytes(), []byte{'\n'}),
		[]byte{'\n'},
	)
	var corruptedEvent protocol.TransferEvent
	if err := json.Unmarshal(corruptedLines[1], &corruptedEvent); err != nil {
		t.Fatal(err)
	}
	corruptedEvent.RecordSHA256 = strings.Repeat("0", 64)
	corruptedLines[1], _ = json.Marshal(corruptedEvent)
	corruptedBody := append(bytes.Join(corruptedLines, []byte{'\n'}), '\n')

	tests := []struct {
		name    string
		body    []byte
		binding protocol.Binding
	}{
		{
			name:    "truncated",
			body:    bytes.TrimSuffix(export.Body.Bytes(), []byte{'\n'}),
			binding: create.Binding,
		},
		{
			name:    "trailing frame",
			body:    append(append([]byte(nil), export.Body.Bytes()...), []byte("{}\n")...),
			binding: create.Binding,
		},
		{
			name:    "event checksum",
			body:    corruptedBody,
			binding: create.Binding,
		},
		{
			name: "wrong binding",
			body: append([]byte(nil), export.Body.Bytes()...),
			binding: protocol.Binding{
				GameID:         create.Binding.GameID,
				ContentID:      create.Binding.ContentID,
				ContentVersion: create.Binding.ContentVersion,
				ContentHash:    "wrong",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := transferHTTPServer(t)
			response := importTransfer(t, target, test.body, test.binding)
			if response.Code < 400 {
				t.Fatalf("invalid import status: %d %s", response.Code, response.Body.String())
			}
			state := perform(t, target, "/v1/session/get", protocol.SessionRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       create.SessionID,
			})
			if state.Code != http.StatusNotFound {
				t.Fatalf("failed import exposed a Session: %d %s", state.Code, state.Body.String())
			}
		})
	}
}

func TestHTTPTransferRejectsCompressedImport(t *testing.T) {
	server := transferHTTPServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/session/import", strings.NewReader("{}\n"))
	request.Header.Set("Content-Type", "application/x-ndjson")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("compressed import status = %d, want 415", response.Code)
	}
}

func TestHTTPTransferExportEndsWithErrorAfterStreamingStarts(t *testing.T) {
	memory := store.NewMemory()
	engine, err := rinruntime.Open(
		&failingRangeStore{Store: memory, ranges: memory},
		policy.Deterministic{},
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httpapi.New(engine, httpapi.Options{})
	create := apiCreateRequest()
	create.SessionID = "session.http-transfer-error"
	create.RequestID = "create.http-transfer-error"
	if response := perform(t, server, "/v1/session/create", create); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	response := perform(t, server, "/v1/session/export", protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("streaming response status = %d, want 200", response.Code)
	}
	lines := bytes.Split(bytes.TrimSuffix(response.Body.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("streaming failure frame count = %d, want manifest/error", len(lines))
	}
	var terminal protocol.TransferError
	if err := json.Unmarshal(lines[1], &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != protocol.TransferFrameError ||
		terminal.Error.Code != "store_load_failed" {
		t.Fatalf("unexpected terminal failure: %+v", terminal)
	}
}

type failingRangeStore struct {
	rinruntime.Store
	ranges rinruntime.RangeStore
}

func (s *failingRangeStore) Head(sessionID string) (rinruntime.EventAnchor, error) {
	return s.ranges.Head(sessionID)
}

func (*failingRangeStore) LoadRange(
	string,
	uint64,
	uint64,
	int,
) (rinruntime.EventPage, error) {
	return rinruntime.EventPage{}, errors.New("fixture range failure")
}

func transferHTTPServer(t *testing.T) http.Handler {
	t.Helper()
	fileStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fileStore.Close(); err != nil {
			t.Errorf("close transfer store: %v", err)
		}
	})
	engine, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	return httpapi.New(engine, httpapi.Options{})
}

func importTransfer(
	t *testing.T,
	handler http.Handler,
	body []byte,
	binding protocol.Binding,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/session/import", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/x-ndjson")
	request.Header.Set("Rin-Expected-Game-Id", binding.GameID)
	request.Header.Set("Rin-Expected-Content-Id", binding.ContentID)
	request.Header.Set("Rin-Expected-Content-Version", binding.ContentVersion)
	request.Header.Set("Rin-Expected-Content-Hash", binding.ContentHash)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

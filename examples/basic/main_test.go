package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestRunQuickstart(t *testing.T) {
	t.Parallel()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		paths = append(paths, request.URL.Path)
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var identity struct {
			ProtocolVersion string `json:"protocol_version"`
			SessionID       string `json:"session_id"`
			RequestID       string `json:"request_id"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if identity.ProtocolVersion != protocol.Version ||
			identity.SessionID != "quick.session.7" ||
			identity.RequestID == "" {
			t.Errorf("unexpected request identity: %+v", identity)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"ok": true,
			"data": protocol.MutationResult{
				SessionID: identity.SessionID,
				Revision:  uint64(len(paths)),
				HeadHash:  strings.Repeat("a", 64),
			},
		})
	}))
	defer server.Close()

	client, err := newQuickClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	var output strings.Builder
	if err := runQuickstart(client, &output, 7); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(paths, ","), "/v2/session/create,/v2/session/observe"; got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	if !strings.Contains(output.String(), "observed revision 2") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestQuickClientRejectsUnsafeRemoteHTTP(t *testing.T) {
	t.Parallel()
	if _, err := newQuickClient("http://example.com", "token"); err == nil {
		t.Fatal("remote plaintext HTTP was accepted")
	}
}

package httpapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestHTTPTransferRoundTripsLineageLargerThanInlineSnapshotLimit(t *testing.T) {
	const sessionID = "session.transfer-over-inline-limit"
	create := apiCreateRequest()
	create.SessionID = sessionID
	create.RequestID = "create.transfer-over-inline-limit"

	seedStore := store.NewMemory()
	seedEngine, err := rinruntime.Open(seedStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedEngine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	events, err := seedStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("seed event count = %d, want 1", len(events))
	}
	event := events[0]
	event.Data = addTransferPadding(
		t,
		event.Data,
		rinruntime.MaxInlineSnapshotBytes+1,
	)
	event.Hash = hashTransferFixtureEvent(t, event)

	sourceStore, err := store.OpenFile(filepath.Join(t.TempDir(), "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer sourceStore.Close()
	if err := sourceStore.Create(sessionID, event); err != nil {
		t.Fatal(err)
	}
	sourceEngine, err := rinruntime.Open(sourceStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	sourceServer := newProductionTestServer(
		httpapi.New(sourceEngine, httpapi.Options{}),
	)
	defer sourceServer.Close()

	requestBody, err := json.Marshal(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	exportRequest, err := http.NewRequest(
		http.MethodPost,
		sourceServer.URL+"/v2/session/export",
		bytes.NewReader(requestBody),
	)
	if err != nil {
		t.Fatal(err)
	}
	exportRequest.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Minute}
	exportResponse, err := client.Do(exportRequest)
	if err != nil {
		t.Fatal(err)
	}
	if exportResponse.StatusCode != http.StatusOK {
		defer exportResponse.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(exportResponse.Body, 4096))
		t.Fatalf("export: %d %s", exportResponse.StatusCode, body)
	}
	transferPath := filepath.Join(t.TempDir(), "session.ndjson")
	transferFile, err := os.Create(transferPath)
	if err != nil {
		t.Fatal(err)
	}
	written, copyErr := io.Copy(transferFile, exportResponse.Body)
	closeErr := exportResponse.Body.Close()
	fileCloseErr := transferFile.Close()
	if copyErr != nil || closeErr != nil || fileCloseErr != nil {
		t.Fatalf(
			"stream export: copy=%v response-close=%v file-close=%v",
			copyErr,
			closeErr,
			fileCloseErr,
		)
	}
	if written <= int64(rinruntime.MaxInlineSnapshotBytes) {
		t.Fatalf(
			"transfer size = %d, want more than inline limit %d",
			written,
			rinruntime.MaxInlineSnapshotBytes,
		)
	}

	targetStore, err := store.OpenFile(filepath.Join(t.TempDir(), "target"))
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	targetEngine, err := rinruntime.Open(targetStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	targetServer := newProductionTestServer(
		httpapi.New(targetEngine, httpapi.Options{}),
	)
	defer targetServer.Close()
	importFile, err := os.Open(transferPath)
	if err != nil {
		t.Fatal(err)
	}
	importRequest, err := http.NewRequest(
		http.MethodPost,
		targetServer.URL+"/v2/session/import",
		importFile,
	)
	if err != nil {
		importFile.Close()
		t.Fatal(err)
	}
	importRequest.Header.Set("Content-Type", "application/x-ndjson")
	importRequest.Header.Set("Rin-Expected-Game-Id", create.Binding.GameID)
	importRequest.Header.Set("Rin-Expected-Content-Id", create.Binding.ContentID)
	importRequest.Header.Set(
		"Rin-Expected-Content-Version",
		create.Binding.ContentVersion,
	)
	importRequest.Header.Set("Rin-Expected-Content-Hash", create.Binding.ContentHash)
	importResponse, err := client.Do(importRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer importResponse.Body.Close()
	importBody, err := io.ReadAll(io.LimitReader(importResponse.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if importResponse.StatusCode != http.StatusOK {
		t.Fatalf("import: %d %s", importResponse.StatusCode, importBody)
	}

	imported, err := targetEngine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Revision != 1 || imported.HeadHash != event.Hash {
		t.Fatalf("imported boundary = %d/%s", imported.Revision, imported.HeadHash)
	}
	replayed, err := targetEngine.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		Revision:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.State.HeadHash != event.Hash {
		t.Fatal("replay did not retain the imported terminal anchor")
	}
	resumed, err := targetEngine.Observe(apiObserveRequest(
		sessionID,
		"observe.after-large-transfer",
		"event.after-large-transfer",
		1,
		[]string{"npc.http"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Revision != 2 {
		t.Fatalf("resumed revision = %d, want 2", resumed.Revision)
	}
}

func newProductionTestServer(handler http.Handler) *httptest.Server {
	server := httptest.NewUnstartedServer(handler)
	server.Config = httpapi.NewProductionServer(
		server.Listener.Addr().String(),
		handler,
	)
	server.Start()
	return server
}

func addTransferPadding(
	t *testing.T,
	data []byte,
	size int,
) []byte {
	t.Helper()
	if len(data) == 0 || data[len(data)-1] != '}' {
		t.Fatal("seed event Data is not a JSON object")
	}
	result := make([]byte, 0, len(data)+size+16)
	result = append(result, data[:len(data)-1]...)
	result = append(result, []byte(`,"padding":"`)...)
	result = append(result, bytes.Repeat([]byte{'x'}, size)...)
	result = append(result, '"', '}')
	if !json.Valid(result) {
		t.Fatal("padded fixture Data is invalid")
	}
	return result
}

func hashTransferFixtureEvent(
	t *testing.T,
	event protocol.EventRecord,
) string {
	t.Helper()
	payload, err := json.Marshal(struct {
		Sequence   uint64          `json:"sequence"`
		Type       string          `json:"type"`
		RequestID  string          `json:"request_id"`
		PrevHash   string          `json:"prev_hash"`
		RecordedAt string          `json:"recorded_at"`
		Data       json.RawMessage `json:"data"`
	}{
		Sequence:   event.Sequence,
		Type:       event.Type,
		RequestID:  event.RequestID,
		PrevHash:   event.PrevHash,
		RecordedAt: event.RecordedAt,
		Data:       event.Data,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

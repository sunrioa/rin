package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestSessionStateLimitRejectsCreateBeforeStoreWrite(t *testing.T) {
	eventStore := store.NewMemory()
	engine, err := rinruntime.OpenWithOptions(
		eventStore,
		cognition.Deterministic{},
		rinruntime.EngineOptions{MaxSessionStateBytes: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session.state-limit-create"
	if _, err := engine.CreateSession(
		createRequest(sessionID),
	); rinruntime.ErrorCode(err) != "state_too_large" ||
		!errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("create State limit error = %v", err)
	}
	if sessions, err := eventStore.ListSessions(); err != nil {
		t.Fatal(err)
	} else if len(sessions) != 0 {
		t.Fatalf("oversized Create persisted Sessions: %v", sessions)
	}
}

func TestSessionStateLimitRejectsMutationBeforeAppend(t *testing.T) {
	eventStore := store.NewMemory()
	setup := newEngine(t, eventStore, cognition.Deterministic{})
	const sessionID = "session.state-limit-mutation"
	if _, err := setup.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	state, err := setup.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.OpenWithOptions(
		eventStore,
		cognition.Deterministic{},
		rinruntime.EngineOptions{
			MaxSessionStateBytes: uint64(len(encoded) + 100),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := observeRequest(
		sessionID,
		"observe.state-limit",
		"event.state-limit",
		1,
	)
	request.Summary = strings.Repeat("s", 1000)
	request.Quote = strings.Repeat("q", 500)
	if _, err := engine.Observe(request); rinruntime.ErrorCode(err) != "state_too_large" {
		t.Fatalf("mutation State limit error = %v", err)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("oversized mutation persisted %d events, want only Create", len(events))
	}
	unchanged, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 {
		t.Fatalf("oversized mutation changed live revision to %d", unchanged.Revision)
	}
}

func TestSessionStateLimitRejectsOversizedDurableReplay(t *testing.T) {
	eventStore := store.NewMemory()
	setup := newEngine(t, eventStore, cognition.Deterministic{})
	const sessionID = "session.state-limit-replay"
	if _, err := setup.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	state, err := setup.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.OpenWithOptions(
		eventStore,
		cognition.Deterministic{},
		rinruntime.EngineOptions{
			MaxSessionStateBytes: uint64(len(encoded) - 1),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.State(
		sessionRequest(sessionID),
	); rinruntime.ErrorCode(err) != "replay_failed" {
		t.Fatalf("oversized durable replay error = %v", err)
	}
}

func TestSessionStateLimitRejectsTransferEventBeforeStaging(t *testing.T) {
	source := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	const sessionID = "session.state-limit-transfer"
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	state, err := source.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingTransferSink{}
	if err := source.ExportTransfer(
		context.Background(),
		sessionRequest(sessionID),
		sink,
	); err != nil {
		t.Fatal(err)
	}

	targetStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	target, err := rinruntime.OpenWithOptions(
		targetStore,
		cognition.Deterministic{},
		rinruntime.EngineOptions{
			MaxSessionStateBytes: uint64(len(encoded) - 1),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := target.BeginTransferImport(
		sink.manifest,
		sink.manifest.Binding,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteEvent(
		sink.events[0],
	); rinruntime.ErrorCode(err) != "state_too_large" {
		t.Fatalf("Transfer State limit error = %v", err)
	}
	if err := writer.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := target.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	}); !errors.Is(err, rinruntime.ErrNotFound) {
		t.Fatalf("oversized Transfer became visible: %v", err)
	}
}

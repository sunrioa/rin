package extension

import (
	"context"
	"errors"
	"testing"
)

type memoryIndexFixture struct {
	replacedSession string
	documents       []MemoryDocument
	matches         []MemoryMatch
	deletedSession  string
	searchError     error
}

func (index *memoryIndexFixture) ReplaceSession(
	_ context.Context,
	sessionID string,
	documents []MemoryDocument,
) error {
	index.replacedSession = sessionID
	index.documents = make([]MemoryDocument, len(documents))
	for position, document := range documents {
		index.documents[position] = cloneMemoryDocument(document)
	}
	if len(documents) > 0 {
		documents[0].Text = "provider mutation"
		documents[0].SourceEventIDs[0] = "event.provider-mutation"
	}
	return nil
}

func (index *memoryIndexFixture) Search(
	_ context.Context,
	_ MemoryQuery,
) ([]MemoryMatch, error) {
	return index.matches, index.searchError
}

func (index *memoryIndexFixture) DeleteSession(
	_ context.Context,
	sessionID string,
) error {
	index.deletedSession = sessionID
	return nil
}

func TestMemoryIndexIsDerivedRebuildableAndProvenanced(t *testing.T) {
	index := &memoryIndexFixture{}
	sourceEvents := []string{"event.memory.1"}
	documents := []MemoryDocument{{
		ID: "memory.document.1", SessionID: "session.memory",
		ActorID: "actor.memory", Text: "The gate opened after the blue switch.",
		SourceEventIDs: sourceEvents, StartTick: 4, EndTick: 5,
		Tags: []string{"quest"},
	}}
	if err := RebuildMemoryIndex(
		context.Background(),
		index,
		"session.memory",
		documents,
	); err != nil {
		t.Fatal(err)
	}
	if documents[0].Text != "The gate opened after the blue switch." ||
		sourceEvents[0] != "event.memory.1" {
		t.Fatal("MemoryIndex mutated authoritative caller data")
	}
	if index.replacedSession != "session.memory" ||
		len(index.documents) != 1 ||
		index.documents[0].TextSHA256 != textHash(index.documents[0].Text) ||
		index.documents[0].SourceEventIDs[0] != "event.memory.1" {
		t.Fatalf("invalid derived document: %+v", index.documents)
	}

	index.matches = []MemoryMatch{{DocumentID: "memory.document.1", Score: 0.75}}
	matches, err := SearchMemory(context.Background(), index, MemoryQuery{
		SessionID: "session.memory", ActorID: "actor.memory",
		Text: "How did the gate open?", Limit: 4,
	})
	if err != nil || len(matches) != 1 ||
		matches[0].DocumentID != "memory.document.1" {
		t.Fatalf("unexpected memory matches: %+v, %v", matches, err)
	}
	matches[0].DocumentID = "caller.mutation"
	if index.matches[0].DocumentID != "memory.document.1" {
		t.Fatal("SearchMemory returned provider-owned result storage")
	}
	if err := DeleteMemoryIndex(
		context.Background(),
		index,
		"session.memory",
	); err != nil {
		t.Fatal(err)
	}
	if index.deletedSession != "session.memory" {
		t.Fatal("derived Session index was not deleted")
	}
}

func TestMemoryIndexFailsClosedOnInvalidProjectionAndResults(t *testing.T) {
	index := &memoryIndexFixture{}
	invalid := MemoryDocument{
		ID: "memory.document.1", SessionID: "session.memory",
		ActorID: "actor.memory", Text: "Missing provenance.",
		StartTick: 1, EndTick: 1,
	}
	if err := RebuildMemoryIndex(
		context.Background(),
		index,
		"session.memory",
		[]MemoryDocument{invalid},
	); err == nil || index.replacedSession != "" {
		t.Fatalf("invalid memory document reached provider: %v", err)
	}
	index.matches = []MemoryMatch{
		{DocumentID: "memory.duplicate", Score: 0.5},
		{DocumentID: "memory.duplicate", Score: 0.4},
	}
	if _, err := SearchMemory(context.Background(), index, MemoryQuery{
		SessionID: "session.memory", ActorID: "actor.memory",
		Text: "query", Limit: 2,
	}); err == nil {
		t.Fatal("duplicate provider results were accepted")
	}
	index.searchError = errors.New("index unavailable")
	if _, err := SearchMemory(context.Background(), index, MemoryQuery{
		SessionID: "session.memory", ActorID: "actor.memory",
		Text: "query", Limit: 2,
	}); !errors.Is(err, index.searchError) {
		t.Fatalf("provider error changed: %v", err)
	}
}

func TestMemoryIndexOperationsRejectNilContextBeforeProvider(t *testing.T) {
	index := &memoryIndexFixture{}
	document := MemoryDocument{
		ID: "memory.document.1", SessionID: "session.memory",
		ActorID: "actor.memory", Text: "Remember this.",
		SourceEventIDs: []string{"event.memory.1"},
		StartTick:      1,
		EndTick:        1,
	}
	if err := RebuildMemoryIndex(
		nil,
		index,
		"session.memory",
		[]MemoryDocument{document},
	); err == nil {
		t.Fatal("RebuildMemoryIndex accepted a nil context")
	}
	if _, err := SearchMemory(nil, index, MemoryQuery{
		SessionID: "session.memory", ActorID: "actor.memory",
		Text: "query", Limit: 1,
	}); err == nil {
		t.Fatal("SearchMemory accepted a nil context")
	}
	if err := DeleteMemoryIndex(nil, index, "session.memory"); err == nil {
		t.Fatal("DeleteMemoryIndex accepted a nil context")
	}
	if index.replacedSession != "" || index.deletedSession != "" {
		t.Fatal("nil context reached MemoryIndex provider")
	}
}

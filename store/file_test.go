package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestFileStoreReplaysAndDetectsTamper(t *testing.T) {
	directory := t.TempDir()
	fileStore, err := store.OpenFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	request := fileCreateRequest()
	if _, err := engine.CreateSession(request); err != nil {
		t.Fatal(err)
	}
	createdEvents, err := fileStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Create(request.SessionID, createdEvents[0]); err != nil {
		t.Fatalf("exact create retry should confirm durability: %v", err)
	}
	for name, nonExact := range nonExactEventRetries(createdEvents[0]) {
		t.Run("create-"+name, func(t *testing.T) {
			if err := fileStore.Create(request.SessionID, nonExact); !errors.Is(err, rinruntime.ErrConflict) {
				t.Fatalf("non-exact create retry should conflict: %v", err)
			}
		})
	}
	differentCreate := createdEvents[0]
	differentCreate.Hash = strings.Repeat("c", 64)
	if err := fileStore.Create(request.SessionID, differentCreate); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("different create event should conflict: %v", err)
	}
	if _, err := engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       request.SessionID,
		RequestID:       "observe.file",
		EventID:         "event.file",
		Tick:            1,
		ObserverIDs:     []string{"npc.one"},
		Source:          "game",
		Kind:            "world",
		Summary:         "A bell rang.",
		Importance:      2,
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: request.SessionID})
	if err != nil || state.Revision != 2 || len(state.Actors["npc.one"].Memories) != 1 {
		t.Fatalf("unexpected replay state: %+v err=%v", state, err)
	}

	logPath := filepath.Join(directory, "sessions", request.SessionID, "events.jsonl")
	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := strings.Replace(string(payload), `"hash":"`, `"hash":"tampered`, 1)
	if err := os.WriteFile(logPath, []byte(corrupted), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rinruntime.Open(fileStore, policy.Deterministic{}); err == nil {
		t.Fatal("tampered event log should fail replay")
	}
}

func TestFileStoreRejectsTraversal(t *testing.T) {
	fileStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileStore.Load("../outside"); err == nil {
		t.Fatal("path traversal should be rejected")
	}
}

func TestFileStoreAppendIsIdempotentAndChecksExpectedHead(t *testing.T) {
	fileStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	request := fileCreateRequest()
	if _, err := engine.CreateSession(request); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       request.SessionID,
		RequestID:       "observe.file-idempotent",
		EventID:         "event.file-idempotent",
		Tick:            1,
		ObserverIDs:     []string{"npc.one"},
		Source:          "game",
		Kind:            "world",
		Summary:         "A durable bell rang.",
		Importance:      2,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := fileStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	tail := events[len(events)-1]
	if err := fileStore.Append(request.SessionID, tail); err != nil {
		t.Fatalf("exact append retry should be idempotent: %v", err)
	}
	for name, nonExact := range nonExactEventRetries(tail) {
		t.Run("append-"+name, func(t *testing.T) {
			if err := fileStore.Append(request.SessionID, nonExact); !errors.Is(err, rinruntime.ErrConflict) {
				t.Fatalf("non-exact append retry should conflict: %v", err)
			}
		})
	}
	afterRetry, err := fileStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRetry) != len(events) {
		t.Fatalf("exact append retry added a duplicate line: before=%d after=%d", len(events), len(afterRetry))
	}
	if !reflect.DeepEqual(afterRetry, events) {
		t.Fatalf("exact append retry changed the log:\nbefore=%+v\nafter=%+v", events, afterRetry)
	}
	baseline := append([]protocol.EventRecord(nil), afterRetry...)
	conflict := tail
	conflict.Hash = strings.Repeat("f", 64)
	if err := fileStore.Append(request.SessionID, conflict); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("different event at the current sequence should conflict: %v", err)
	}
	wrongHead := tail
	wrongHead.Sequence++
	wrongHead.Hash = strings.Repeat("e", 64)
	wrongHead.PrevHash = strings.Repeat("d", 64)
	if err := fileStore.Append(request.SessionID, wrongHead); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("append with an unexpected previous hash should conflict: %v", err)
	}
	afterConflicts, err := fileStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterConflicts, baseline) {
		t.Fatalf("conflicting appends mutated the file log:\nbefore=%+v\nafter=%+v", baseline, afterConflicts)
	}
}

func TestFileStoreLoadRejectsIncompleteTail(t *testing.T) {
	root := t.TempDir()
	fileStore, err := store.OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	request := fileCreateRequest()
	if _, err := engine.CreateSession(request); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "sessions", request.SessionID, "events.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"sequence":2`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fileStore.Load(request.SessionID); !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("Load should reject an incomplete tail as corruption, got %v", err)
	}
}

func TestMemoryStoreAppendIsIdempotentAndChecksExpectedHead(t *testing.T) {
	memoryStore := store.NewMemory()
	engine, err := rinruntime.Open(memoryStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	request := fileCreateRequest()
	if _, err := engine.CreateSession(request); err != nil {
		t.Fatal(err)
	}
	events, err := memoryStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	tail := events[len(events)-1]
	if err := memoryStore.Create(request.SessionID, tail); err != nil {
		t.Fatalf("exact create retry should confirm durability: %v", err)
	}
	for name, nonExact := range nonExactEventRetries(tail) {
		t.Run("create-"+name, func(t *testing.T) {
			if err := memoryStore.Create(request.SessionID, nonExact); !errors.Is(err, rinruntime.ErrConflict) {
				t.Fatalf("non-exact create retry should conflict: %v", err)
			}
		})
	}
	differentCreate := tail
	differentCreate.Hash = strings.Repeat("c", 64)
	if err := memoryStore.Create(request.SessionID, differentCreate); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("different create event should conflict: %v", err)
	}
	if err := memoryStore.Append(request.SessionID, tail); err != nil {
		t.Fatalf("exact append retry should be idempotent: %v", err)
	}
	for name, nonExact := range nonExactEventRetries(tail) {
		t.Run("append-"+name, func(t *testing.T) {
			if err := memoryStore.Append(request.SessionID, nonExact); !errors.Is(err, rinruntime.ErrConflict) {
				t.Fatalf("non-exact append retry should conflict: %v", err)
			}
		})
	}
	conflict := tail
	conflict.Hash = strings.Repeat("f", 64)
	if err := memoryStore.Append(request.SessionID, conflict); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("different event at current sequence should conflict: %v", err)
	}
	wrongHead := tail
	wrongHead.Sequence++
	wrongHead.Hash = strings.Repeat("e", 64)
	wrongHead.PrevHash = strings.Repeat("d", 64)
	if err := memoryStore.Append(request.SessionID, wrongHead); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("unexpected previous hash should conflict: %v", err)
	}
	after, err := memoryStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, events) {
		t.Fatalf("memory-store retries/conflicts mutated the log:\nbefore=%+v\nafter=%+v", events, after)
	}
}

func TestSnapshotFileIsPrivate(t *testing.T) {
	directory := t.TempDir()
	fileStore, _ := store.OpenFile(directory)
	engine, _ := rinruntime.Open(fileStore, policy.Deterministic{})
	request := fileCreateRequest()
	_, _ = engine.CreateSession(request)
	if _, err := engine.Snapshot(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: request.SessionID}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Snapshot(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: request.SessionID}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(directory, "sessions", request.SessionID, "snapshot-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one snapshot, got %v err=%v", matches, err)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot permissions are %o, expected 600", info.Mode().Perm())
	}
}

func fileCreateRequest() protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.file",
		SessionID:       "session.file",
		Binding:         protocol.Binding{GameID: "game.test", ContentID: "base", ContentVersion: "1", ContentHash: "hash"},
		Actors: []protocol.ActorSeed{{
			ID: "npc.one", Kind: "npc", DisplayName: "One", ThinkEveryTicks: 1, Enabled: true,
			Goals: []protocol.Goal{{ID: "goal.one", Description: "Wait", Priority: 1, TargetProgress: 1, Status: "active"}},
		}},
	}
}

func nonExactEventRetries(event protocol.EventRecord) map[string]protocol.EventRecord {
	data := event
	data.Data = append(append([]byte(nil), event.Data...), ' ')
	eventType := event
	eventType.Type += ".tampered"
	requestID := event
	requestID.RequestID += ".tampered"
	prevHash := event
	prevHash.PrevHash += "0"
	recordedAt := event
	recordedAt.RecordedAt += "0"
	return map[string]protocol.EventRecord{
		"data-bytes":  data,
		"type":        eventType,
		"request-id":  requestID,
		"prev-hash":   prevHash,
		"recorded-at": recordedAt,
	}
}

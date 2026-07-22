package store_test

import (
	"os"
	"path/filepath"
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

package runtime_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestMemorySessionLifecycleIsReadOnlyAndRetired(t *testing.T) {
	t.Parallel()
	assertSessionLifecycle(t, store.NewMemory(), nil)
}

func TestFileSessionLifecycleSurvivesRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files, err := store.OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionLifecycle(t, files, func(
		t *testing.T,
		deleteRequest protocol.DeleteSessionRequest,
	) {
		if err := files.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := store.OpenFile(root)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		engine, err := rinruntime.Open(reopened, policy.Deterministic{})
		if err != nil {
			t.Fatal(err)
		}
		duplicate, err := engine.DeleteSession(deleteRequest)
		if err != nil {
			t.Fatal(err)
		}
		if !duplicate.Duplicate {
			t.Fatal("restart lost exact deletion receipt")
		}
		_, err = engine.CreateSession(lifecycleCreateRequest())
		if rinruntime.ErrorCode(err) != "session_retired" {
			t.Fatalf("retired Session was reusable after restart: %v", err)
		}
	})
}

func assertSessionLifecycle(
	t *testing.T,
	eventStore rinruntime.Store,
	afterDelete func(*testing.T, protocol.DeleteSessionRequest),
) {
	t.Helper()
	engine, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := engine.CreateSession(lifecycleCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	stats, err := engine.SessionStats(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       created.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Lifecycle != "active" ||
		stats.EventCount != 1 ||
		stats.Bytes.EventLog == 0 ||
		stats.Bytes.Total < stats.Bytes.EventLog {
		t.Fatalf("unexpected active stats: %+v", stats)
	}
	binding := lifecycleCreateRequest().Binding
	archiveRequest := protocol.ArchiveSessionRequest{
		ProtocolVersion:  protocol.Version,
		SessionID:        created.SessionID,
		RequestID:        "archive.lifecycle",
		ExpectedBinding:  binding,
		ExpectedRevision: created.Revision,
		ExpectedHeadHash: created.HeadHash,
	}
	archived, err := engine.ArchiveSession(archiveRequest)
	if err != nil {
		t.Fatal(err)
	}
	duplicateArchive, err := engine.ArchiveSession(archiveRequest)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ReceiptID == "" || !duplicateArchive.Duplicate ||
		duplicateArchive.ReceiptID != archived.ReceiptID {
		t.Fatalf("archive was not idempotent: first=%+v retry=%+v", archived, duplicateArchive)
	}
	_, err = engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       created.SessionID,
		RequestID:       "observe.archived",
		EventID:         "event.archived",
		Tick:            1,
		ObserverIDs:     []string{"npc.lifecycle"},
		Source:          "player",
		Kind:            "dialogue",
		Summary:         "This must not be recorded.",
		Importance:      1,
	})
	if rinruntime.ErrorCode(err) != "session_archived" {
		t.Fatalf("archived mutation error = %v", err)
	}
	state, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       created.SessionID,
	})
	if err != nil || state.Revision != created.Revision {
		t.Fatalf("archived read failed: state=%+v err=%v", state, err)
	}
	archivedStats, err := engine.SessionStats(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       created.SessionID,
	})
	if err != nil || archivedStats.Lifecycle != "archived" {
		t.Fatalf("archived stats = %+v, %v", archivedStats, err)
	}
	deleteRequest := protocol.DeleteSessionRequest{
		ProtocolVersion:  protocol.Version,
		SessionID:        created.SessionID,
		RequestID:        "delete.lifecycle",
		ExpectedBinding:  binding,
		ExpectedRevision: created.Revision,
		ExpectedHeadHash: created.HeadHash,
		ArchiveReceiptID: archived.ReceiptID,
		Confirmation:     created.SessionID,
	}
	deleted, err := engine.DeleteSession(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Duplicate || !strings.HasPrefix(deleted.ReceiptID, "delete.") {
		t.Fatalf("unexpected delete receipt: %+v", deleted)
	}
	duplicateDelete, err := engine.DeleteSession(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateDelete.Duplicate ||
		duplicateDelete.ReceiptID != deleted.ReceiptID {
		t.Fatalf("delete was not idempotent: %+v", duplicateDelete)
	}
	_, err = engine.CreateSession(lifecycleCreateRequest())
	if rinruntime.ErrorCode(err) != "session_retired" &&
		!errors.Is(err, rinruntime.ErrRetired) {
		t.Fatalf("retired Session was reusable: %v", err)
	}
	if afterDelete != nil {
		afterDelete(t, deleteRequest)
	}
}

func lifecycleCreateRequest() protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.lifecycle",
		SessionID:       "session.lifecycle",
		Binding: protocol.Binding{
			GameID:         "game.lifecycle",
			ContentID:      "base",
			ContentVersion: "1",
			ContentHash:    "hash",
		},
		Features: []string{protocol.FeatureOutcomeReporting},
		Actors: []protocol.ActorSeed{{
			ID:              "npc.lifecycle",
			Kind:            "npc",
			DisplayName:     "Lifecycle",
			ThinkEveryTicks: 5,
			Enabled:         true,
		}},
	}
}

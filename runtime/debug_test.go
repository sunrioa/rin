package runtime_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rintime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestTimelineIsBoundedAndRedacted(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.timeline"))
	observation := observeRequest("session.timeline", "observe.timeline", "event.timeline", 1)
	observation.Summary = "SECRET_SUMMARY player disclosed a private concern"
	observation.Quote = "SECRET_QUOTE exact player words"
	if _, err := engine.Observe(observation); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(context.Background(), proposeRequest("session.timeline", "propose.timeline", 2, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.timeline", RequestID: "commit.timeline",
		ProposalID: proposal.ID, EventID: "event.commit.timeline", Tick: 2, Accepted: true,
		Outcome: "SECRET_OUTCOME model-authored response", Tags: []string{"conversation"},
	}); err != nil {
		t.Fatal(err)
	}

	page, err := engine.Timeline(protocol.TimelineRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.timeline", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || !page.HasMore || page.NextAfterRevision != 2 || page.CurrentRevision != 4 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	second, err := engine.Timeline(protocol.TimelineRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.timeline",
		AfterRevision: page.NextAfterRevision, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 2 || second.HasMore || second.Entries[1].Status != "accepted" {
		t.Fatalf("unexpected second page: %+v", second)
	}
	payload, err := json.Marshal([]protocol.TimelineResponse{page, second})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET_SUMMARY", "SECRET_QUOTE", "SECRET_OUTCOME"} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("timeline leaked %s: %s", secret, payload)
		}
	}
}

func TestReplayUsesExactRevisionWithoutMutatingCurrentState(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.replay"))
	_, _ = engine.Observe(observeRequest("session.replay", "observe.replay.1", "event.replay.1", 3))
	_, _ = engine.Observe(observeRequest("session.replay", "observe.replay.2", "event.replay.2", 5))

	snapshot, err := engine.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.replay", Revision: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Revision != 2 || snapshot.State.Tick != 3 || len(snapshot.State.Actors["npc.mira"].Memories) != 1 {
		t.Fatalf("unexpected replay state: %+v", snapshot.State)
	}
	if err := rintime.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("replay did not return a valid snapshot: %v", err)
	}
	current, err := engine.State(sessionRequest("session.replay"))
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 3 || current.Tick != 5 || len(current.Actors["npc.mira"].Memories) != 2 {
		t.Fatalf("replay mutated current state: %+v", current)
	}
	_, err = engine.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.replay", Revision: 99,
	})
	if rintime.ErrorCode(err) != "revision_not_found" {
		t.Fatalf("expected revision_not_found, got %v", err)
	}
}

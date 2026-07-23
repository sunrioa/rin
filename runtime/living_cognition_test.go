package runtime_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestLivingMemoryArchivesAndReplaysDeterministically(t *testing.T) {
	eventStore := store.NewMemory()
	engine := newEngine(t, eventStore, policy.Deterministic{})
	create := createRequest("session.archive")
	create.Features = append(create.Features, protocol.FeatureMemoryArchive)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 145; index++ {
		request := observeRequest("session.archive", fmt.Sprintf("observe.%d", index), fmt.Sprintf("event.%d", index), int64(index))
		request.Importance = 1
		request.Tags = []string{"recent"}
		request.Summary = fmt.Sprintf("Recent event %d", index)
		if index <= 64 {
			request.Importance = 5
			request.Tags = []string{"foundation"}
			request.Summary = fmt.Sprintf("Foundational event %d", index)
		}
		if index <= 16 {
			request.Tags = []string{"archive"}
		}
		if _, err := engine.Observe(request); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engine.State(sessionRequest("session.archive"))
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actors["npc.mira"]
	if len(actor.Memories) > 128 || len(actor.MemorySummaries) != 2 {
		t.Fatalf("unexpected archive bounds: memories=%d summaries=%d", len(actor.Memories), len(actor.MemorySummaries))
	}
	for _, summary := range actor.MemorySummaries {
		if summary.Reason != "episodic_capacity" || len(summary.SourceEventIDs) != 16 || !strings.HasPrefix(summary.ID, "summary.") {
			t.Fatalf("unexpected memory summary: %+v", summary)
		}
	}

	proposalRequest := proposeRequest("session.archive", "propose.archive", 1000, []string{"archive"})
	proposal, _, err := engine.Propose(context.Background(), proposalRequest)
	if err != nil {
		t.Fatal(err)
	}
	foundSummary := false
	for _, id := range proposal.RecalledMemoryIDs {
		foundSummary = foundSummary || strings.HasPrefix(id, "summary.")
	}
	if !foundSummary {
		t.Fatalf("expected policy to recall an archived summary, got %v", proposal.RecalledMemoryIDs)
	}

	duplicateEvent := observeRequest("session.archive", "observe.duplicate-event", "event.1", 145)
	if _, err := engine.Observe(duplicateEvent); err == nil || rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("compacted source event should remain protected from duplication: %v", err)
	}

	before, err := engine.Snapshot(sessionRequest("session.archive"))
	if err != nil {
		t.Fatal(err)
	}
	reopened := newEngine(t, eventStore, policy.Deterministic{})
	after, err := reopened.Snapshot(sessionRequest("session.archive"))
	if err != nil {
		t.Fatal(err)
	}
	if before.StateHash != after.StateHash {
		t.Fatalf("archive replay changed state hash: before=%s after=%s", before.StateHash, after.StateHash)
	}
}

func TestBeliefConflictsRemainActorLocal(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	create := createRequest("session.beliefs")
	create.Features = append(create.Features, protocol.FeatureBeliefConflicts)
	second := create.Actors[0]
	second.ID = "npc.oren"
	second.DisplayName = "Oren"
	second.Goals = nil
	create.Actors = append(create.Actors, second)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}

	first := observeRequest("session.beliefs", "observe.rumor-a", "event.rumor-a", 1)
	first.ObserverIDs = []string{"npc.mira", "npc.oren"}
	first.Facts = []protocol.Fact{{
		SubjectID: "relic", Predicate: "location", Object: "harbor", Visibility: []string{"npc.mira"}, Confidence: 80,
	}}
	if _, err := engine.Observe(first); err != nil {
		t.Fatal(err)
	}
	secondRumor := observeRequest("session.beliefs", "observe.rumor-b", "event.rumor-b", 2)
	secondRumor.Facts = []protocol.Fact{{
		SubjectID: "relic", Predicate: "location", Object: "tower", Visibility: []string{"npc.mira"}, Confidence: 60,
	}}
	if _, err := engine.Observe(secondRumor); err != nil {
		t.Fatal(err)
	}

	state, err := engine.State(sessionRequest("session.beliefs"))
	if err != nil {
		t.Fatal(err)
	}
	mira := state.Actors["npc.mira"]
	set := mira.BeliefSets["relic:location"]
	if !set.Conflicted || len(set.Claims) != 2 || mira.Beliefs["relic:location"].Object != "tower" {
		t.Fatalf("unexpected conflicting belief state: set=%+v selected=%+v", set, mira.Beliefs["relic:location"])
	}
	if len(state.Actors["npc.oren"].Beliefs) != 0 || len(state.Actors["npc.oren"].BeliefSets) != 0 {
		t.Fatalf("private claim leaked to another observer: %+v", state.Actors["npc.oren"])
	}
	snapshot, err := engine.Snapshot(sessionRequest("session.beliefs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rinruntime.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("living cognition snapshot should validate: %v", err)
	}
}

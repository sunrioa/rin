package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestEngineEndToEnd(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	created, err := engine.CreateSession(createRequest("session.flow"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Duplicate {
		t.Fatalf("unexpected create result: %+v", created)
	}

	observed, err := engine.Observe(observeRequest("session.flow", "observe.1", "event.1", 1))
	if err != nil {
		t.Fatal(err)
	}
	if observed.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", observed.Revision)
	}

	proposal, duplicate, err := engine.Propose(context.Background(), proposeRequest("session.flow", "propose.1", 2, nil))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate || proposal.Action.ID != "talk" || proposal.GoalID != "goal.connect" {
		t.Fatalf("unexpected proposal: %+v duplicate=%v", proposal, duplicate)
	}
	if len(proposal.RecalledMemoryIDs) != 1 {
		t.Fatalf("expected one recalled memory, got %v", proposal.RecalledMemoryIDs)
	}

	committed, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.flow",
		RequestID:       "commit.1",
		ProposalID:      proposal.ID,
		EventID:         "event.action.1",
		Tick:            2,
		Accepted:        true,
		Outcome:         "Mira asked a careful follow-up question.",
		Tags:            []string{"conversation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Revision != 4 {
		t.Fatalf("expected revision 4, got %d", committed.Revision)
	}

	state, err := engine.State(sessionRequest("session.flow"))
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actors["npc.mira"]
	if actor.NextThinkTick != 7 {
		t.Fatalf("expected next think tick 7, got %d", actor.NextThinkTick)
	}
	if len(actor.Memories) != 2 || actor.Memories[0].RecallCount != 1 {
		t.Fatalf("unexpected memories: %+v", actor.Memories)
	}
	if actor.Goals[0].Progress != 1 {
		t.Fatalf("expected goal progress 1, got %+v", actor.Goals[0])
	}
	if len(actor.RecentActions) != 1 || actor.RecentActions[0].Status != "accepted" {
		t.Fatalf("unexpected recent actions: %+v", actor.RecentActions)
	}

	due, err := engine.DueAgents(protocol.DueAgentsRequest{ProtocolVersion: protocol.Version, SessionID: "session.flow", Tick: 6, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(due.Agents) != 0 {
		t.Fatalf("actor should not be due: %+v", due.Agents)
	}
	due, err = engine.DueAgents(protocol.DueAgentsRequest{ProtocolVersion: protocol.Version, SessionID: "session.flow", Tick: 7, Limit: 10})
	if err != nil || len(due.Agents) != 1 || due.Agents[0].ActorID != "npc.mira" {
		t.Fatalf("actor should be due: %+v err=%v", due, err)
	}

	repeated, err := engine.Observe(observeRequest("session.flow", "observe.1", "event.1", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Duplicate || repeated.Revision != 2 {
		t.Fatalf("expected idempotent result, got %+v", repeated)
	}
}

func TestOutcomeFeatureRejectsAmbiguousCommitUpdates(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	const sessionID = "session.outcome-update-validation"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.outcome-update-validation", 0, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	base := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		ProposalID:      proposal.ID,
		Tick:            proposal.Tick,
	}
	rejected := base
	rejected.RequestID = "commit.rejected-with-updates"
	rejected.EventID = "event.rejected-with-updates"
	rejected.Facts = []protocol.Fact{{
		SubjectID: "player", Predicate: "attempted", Object: "locked-door", Confidence: 100,
	}}
	if _, err := engine.Commit(rejected); rinruntime.ErrorCode(err) != "rejected_outcome_updates" {
		t.Fatalf("rejected outcome updates should fail explicitly, got %v", err)
	}

	duplicate := base
	duplicate.RequestID = "commit.duplicate-goal-updates"
	duplicate.EventID = "event.duplicate-goal-updates"
	duplicate.Accepted = true
	duplicate.Outcome = "The action happened."
	duplicate.GoalUpdates = []protocol.GoalUpdate{
		{GoalID: "goal.connect", ProgressDelta: 1},
		{GoalID: "goal.connect", ProgressDelta: -1},
	}
	if _, err := engine.Commit(duplicate); rinruntime.ErrorCode(err) != "duplicate_goal_update" {
		t.Fatalf("duplicate new-semantics goal updates should fail explicitly, got %v", err)
	}
}

func TestLegacyCommitPreservesRepeatedGoalUpdateBehavior(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	const sessionID = "session.legacy-repeated-goal-update"
	create := createRequest(sessionID)
	create.Features = nil
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.legacy-repeated-goal-update", 0, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.legacy-repeated-goal-update",
		ProposalID:      proposal.ID,
		EventID:         "event.legacy-repeated-goal-update",
		Tick:            proposal.Tick,
		Accepted:        true,
		Outcome:         "The legacy action happened.",
		GoalUpdates: []protocol.GoalUpdate{
			{GoalID: "goal.connect", ProgressDelta: 1},
			{GoalID: "goal.connect", ProgressDelta: 1},
		},
	}); err != nil {
		t.Fatalf("pre-feature repeated goal updates should retain legacy behavior: %v", err)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if progress := state.Actors["npc.mira"].Goals[0].Progress; progress != 3 {
		t.Fatalf("legacy repeated updates produced progress %d, want 3", progress)
	}
}

func TestLegacyStateRejectsInjectedOutcomeOccurrenceMetadata(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	const sessionID = "session.legacy-metadata-gate"
	create := createRequest(sessionID)
	create.Features = nil
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	observation := observeRequest(sessionID, "observe.legacy-metadata", "event.legacy-metadata", 1)
	observation.Facts = []protocol.Fact{{
		SubjectID: "player", Predicate: "respected_boundary", Object: "yes", Confidence: 100,
	}}
	if _, err := engine.Observe(observation); err != nil {
		t.Fatal(err)
	}
	baseline, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateSessionState(baseline); err != nil {
		t.Fatalf("legacy baseline state is invalid: %v", err)
	}

	withGoalMetadata := baseline
	actor := withGoalMetadata.Actors["npc.mira"]
	actor.Goals = append([]protocol.Goal(nil), actor.Goals...)
	actor.Goals[0].UpdatedTick = 1
	withGoalMetadata.Actors["npc.mira"] = actor
	if err := protocol.ValidateSessionState(withGoalMetadata); err == nil {
		t.Fatal("legacy state accepted injected goal occurrence metadata")
	}

	baseline, err = engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	withFactMetadata := baseline
	actor = withFactMetadata.Actors["npc.mira"]
	actor.Beliefs = make(map[string]protocol.Fact, len(actor.Beliefs))
	for key, fact := range baseline.Actors["npc.mira"].Beliefs {
		actor.Beliefs[key] = fact
	}
	fact := actor.Beliefs["player:respected_boundary"]
	fact.ObservedTick = 1
	actor.Beliefs["player:respected_boundary"] = fact
	withFactMetadata.Actors["npc.mira"] = actor
	if err := protocol.ValidateSessionState(withFactMetadata); err == nil {
		t.Fatal("legacy state accepted injected fact occurrence metadata")
	}
}

func TestBoundaryRequiresSafeCandidate(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.boundary"))
	request := proposeRequest("session.boundary", "propose.boundary", 0, []string{"private"})
	proposal, _, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Action.ID != "refuse" || proposal.Stance != "refuse" {
		t.Fatalf("boundary was not protected: %+v", proposal)
	}

	request.RequestID = "propose.unsafe"
	request.CandidateActions = request.CandidateActions[:1]
	_, _, err = engine.Propose(context.Background(), request)
	if !errors.Is(err, rinruntime.ErrNoSafeAction) || rinruntime.ErrorCode(err) != "no_safe_action" {
		t.Fatalf("expected no safe action, got %v", err)
	}
}

func TestCommitReportsAcceptedOutcomeAfterStateAdvances(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.late-accepted"))
	proposal, _, err := engine.Propose(context.Background(), proposeRequest("session.late-accepted", "propose.late", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Observe(observeRequest("session.late-accepted", "observe.after", "event.after", 5))
	if err != nil {
		t.Fatal(err)
	}
	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.late-accepted",
		RequestID:       "commit.late",
		ProposalID:      proposal.ID,
		EventID:         "event.commit.late",
		Tick:            0,
		Accepted:        true,
		Outcome:         "The game already applied the action.",
	}
	if _, err := engine.Commit(commit); err != nil {
		t.Fatalf("late outcome should be recorded: %v", err)
	}
	state, err := engine.State(sessionRequest(commit.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actors[proposal.ActorID]
	if state.Tick != 5 || state.Proposals[proposal.ID].Status != "accepted" {
		t.Fatalf("late outcome regressed state: %+v", state)
	}
	if len(actor.RecentActions) != 1 || len(actor.Memories) != 2 {
		t.Fatalf("accepted outcome was not applied exactly once: %+v", actor)
	}
	var outcome *protocol.Memory
	for index := range actor.Memories {
		if actor.Memories[index].EventID == commit.EventID {
			outcome = &actor.Memories[index]
			break
		}
	}
	if outcome == nil || outcome.Tick != commit.Tick {
		t.Fatalf("outcome did not preserve its occurrence time: %+v", actor.Memories)
	}
}

func TestCommitReportsRejectedOutcomeAfterStateAdvances(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.late-rejected"))
	proposal, _, err := engine.Propose(context.Background(), proposeRequest("session.late-rejected", "propose.reject", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(observeRequest("session.late-rejected", "observe.after", "event.after", 5)); err != nil {
		t.Fatal(err)
	}
	before, _ := engine.State(sessionRequest("session.late-rejected"))
	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.late-rejected",
		RequestID:       "commit.reject",
		ProposalID:      proposal.ID,
		EventID:         "event.commit.reject",
		Tick:            0,
		Accepted:        false,
		Outcome:         "The game rejected the action.",
	}
	if _, err := engine.Commit(commit); err != nil {
		t.Fatalf("late rejection should be recorded: %v", err)
	}
	after, _ := engine.State(sessionRequest(commit.SessionID))
	if after.Tick != before.Tick || after.Proposals[proposal.ID].Status != "rejected" {
		t.Fatalf("rejected outcome was not settled correctly: %+v", after)
	}
	actorBefore := before.Actors[proposal.ActorID]
	actorAfter := after.Actors[proposal.ActorID]
	if len(actorAfter.Memories) != len(actorBefore.Memories) || len(actorAfter.RecentActions) != len(actorBefore.RecentActions) {
		t.Fatalf("rejected outcome applied accepted-only side effects: before=%+v after=%+v", actorBefore, actorAfter)
	}
}

func TestCommitRejectsTickBeforeProposal(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.commit-tick"))
	proposal, _, err := engine.Propose(context.Background(), proposeRequest("session.commit-tick", "propose.tick", 2, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.commit-tick",
		RequestID:       "commit.tick",
		ProposalID:      proposal.ID,
		EventID:         "event.commit.tick",
		Tick:            1,
		Accepted:        true,
		Outcome:         "Impossible occurrence time.",
	})
	if !errors.Is(err, rinruntime.ErrConflict) || rinruntime.ErrorCode(err) != "tick_regressed" {
		t.Fatalf("expected tick_regressed, got %v", err)
	}
	state, _ := engine.State(sessionRequest("session.commit-tick"))
	if state.Proposals[proposal.ID].Status != "pending" {
		t.Fatalf("invalid outcome resolved proposal: %+v", state.Proposals[proposal.ID])
	}
}

func TestSnapshotTamperAndFreshRestore(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.snapshot"))
	newerObservation := observeRequest("session.snapshot", "observe.snapshot", "event.snapshot", 4)
	newerObservation.Facts = []protocol.Fact{{
		SubjectID: "door", Predicate: "state", Object: "open", Confidence: 80,
	}}
	_, _ = engine.Observe(newerObservation)
	snapshot, err := engine.Snapshot(sessionRequest("session.snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := snapshot
	tampered.State.Tick++
	if err := rinruntime.ValidateSnapshot(tampered); err == nil {
		t.Fatal("tampered snapshot should be rejected")
	}
	invalidState := snapshot.State
	actor := invalidState.Actors["npc.mira"]
	actor.Memories[0].Summary = ""
	invalidState.Actors["npc.mira"] = actor
	if _, err := rinruntime.SnapshotOf(invalidState); err == nil {
		t.Fatal("SnapshotOf should reject structurally invalid state before hashing")
	}
	snapshot, err = engine.Snapshot(sessionRequest("session.snapshot"))
	if err != nil {
		t.Fatal(err)
	}

	restoredEngine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	result, err := restoredEngine.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.snapshot",
		RequestID:       "restore.snapshot",
		ExpectedBinding: snapshot.State.Binding,
		Snapshot:        snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 {
		t.Fatalf("fresh restored log should start at revision 1, got %d", result.Revision)
	}
	state, err := restoredEngine.State(sessionRequest("session.snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Tick != 4 || len(state.Actors["npc.mira"].Memories) != 1 || len(state.Proposals) != 0 {
		t.Fatalf("unexpected restored state: %+v", state)
	}
	reconciliation := observeRequest(
		"session.snapshot",
		"observe.offline-reconciliation",
		"event.offline-reconciliation",
		2,
	)
	reconciliation.Facts = []protocol.Fact{{
		SubjectID: "door", Predicate: "state", Object: "closed", Confidence: 100,
	}}
	if _, err := restoredEngine.Observe(reconciliation); err != nil {
		t.Fatalf("late authoritative reconciliation after restore should succeed: %v", err)
	}
	state, err = restoredEngine.State(sessionRequest("session.snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	actor = state.Actors["npc.mira"]
	if state.Tick != 4 ||
		len(actor.Memories) != 2 ||
		actor.Memories[0].EventID != reconciliation.EventID ||
		actor.Beliefs["door:state"].Object != "open" {
		t.Fatalf("late reconciliation regressed restored state: %+v", state)
	}
	reconciledSnapshot, err := restoredEngine.Snapshot(sessionRequest("session.snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rinruntime.ValidateSnapshot(reconciledSnapshot); err != nil {
		t.Fatalf("reconciled restore snapshot must validate: %v", err)
	}
}

func TestFreshRestoreRetainsPendingProposalForSavedOutcomeOutbox(t *testing.T) {
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	const sessionID = "session.restore-outbox"
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := source.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.restore-outbox", 7, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State.Receipts = make(map[string]protocol.RequestReceipt, 1024)
	for index := 0; index < 1024; index++ {
		requestID := fmt.Sprintf("legacy.receipt.%04d", index)
		revision := uint64(0)
		if index == 1023 {
			revision = snapshot.State.Revision
		}
		snapshot.State.Receipts[requestID] = protocol.RequestReceipt{
			Kind:     rinruntime.EventObserved,
			EntityID: fmt.Sprintf("legacy.event.%04d", index),
			Revision: revision,
		}
	}
	snapshot, err = rinruntime.SnapshotOf(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}

	restored := newEngine(t, store.NewMemory(), policy.Deterministic{})
	restoreRequest := protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.outbox",
		ExpectedBinding: snapshot.State.Binding,
		Snapshot:        snapshot,
	}
	if _, err := restored.Restore(restoreRequest); err != nil {
		t.Fatal(err)
	}
	repeatedRestore, err := restored.Restore(restoreRequest)
	if err != nil || !repeatedRestore.Duplicate {
		t.Fatalf("full-receipt restore retry must be idempotent: result=%+v err=%v", repeatedRestore, err)
	}
	state, err := restored.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if got, exists := state.Proposals[proposal.ID]; !exists || got.Status != "pending" {
		t.Fatalf("restore discarded the saved pending proposal: %+v", state.Proposals)
	}

	commitRequest := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.restore-outbox",
		ProposalID:      proposal.ID,
		EventID:         "event.restore-outbox",
		Tick:            7,
		Accepted:        true,
		Outcome:         "The saved game had already applied this action.",
		GoalUpdates: []protocol.GoalUpdate{{
			GoalID: "goal.connect", ProgressDelta: 4, Status: "completed",
		}},
	}
	firstCommit, err := restored.Commit(commitRequest)
	if err != nil {
		t.Fatalf("saved outcome report after restore failed: %v", err)
	}
	repeatedCommit, err := restored.Commit(commitRequest)
	if err != nil || !repeatedCommit.Duplicate || repeatedCommit.Revision != firstCommit.Revision {
		t.Fatalf("full-receipt outcome retry must be idempotent: first=%+v repeated=%+v err=%v", firstCommit, repeatedCommit, err)
	}
	state, err = restored.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actors[proposal.ActorID]
	goal, found := findGoal(actor, "goal.connect")
	resolved := state.Proposals[proposal.ID]
	if len(actor.RecentActions) != 1 {
		t.Fatalf("restored outbox recent actions = %+v, want one", actor.RecentActions)
	}
	recent := actor.RecentActions[0]
	if !found ||
		goal.ProgressAccumulator != 5 ||
		goal.Progress != 3 ||
		goal.Status != "completed" ||
		!goal.StatusExplicit ||
		goal.UpdatedTick != 7 ||
		goal.StatusUpdatedTick != 7 ||
		goal.StatusSourceEventID != "event.restore-outbox" ||
		actor.NextThinkTick != 12 ||
		resolved.Status != "accepted" ||
		resolved.OutcomeEventID != "event.restore-outbox" ||
		resolved.OutcomeTick != 7 ||
		recent.ID != proposal.ID ||
		recent.Status != "accepted" ||
		recent.OutcomeEventID != "event.restore-outbox" ||
		recent.OutcomeTick != 7 {
		t.Fatalf(
			"restored outbox did not reconcile complete outcome state: actor=%+v goal=%+v proposal=%+v recent=%+v",
			actor,
			goal,
			resolved,
			recent,
		)
	}
	finalSnapshot, err := restored.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if err := rinruntime.ValidateSnapshot(finalSnapshot); err != nil {
		t.Fatalf("restored outcome state must remain snapshot-compatible: %v", err)
	}
}

func TestFreshRestoreRebasesArrivalRevisionsWithinTheNewEventChain(t *testing.T) {
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	const sessionID = "session.restore-revision-generation"
	create := createRequest(sessionID)
	create.Features = append(create.Features, protocol.FeatureBeliefConflicts)
	if _, err := source.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		request := observeRequest(
			sessionID,
			fmt.Sprintf("observe.restore-filler.%d", index),
			fmt.Sprintf("event.restore-filler.%d", index),
			0,
		)
		if _, err := source.Observe(request); err != nil {
			t.Fatal(err)
		}
	}
	oldFact := observeRequest(sessionID, "observe.restore-old", "event.restore-alpha", 5)
	oldFact.Facts = []protocol.Fact{{
		SubjectID: "gate", Predicate: "state", Object: "closed", Confidence: 80,
	}}
	if _, err := source.Observe(oldFact); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}

	restored := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := restored.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.revision-generation",
		ExpectedBinding: snapshot.State.Binding,
		Snapshot:        snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	newFact := observeRequest(sessionID, "observe.restore-new", "event.restore-zulu", 5)
	newFact.Facts = []protocol.Fact{{
		SubjectID: "gate", Predicate: "state", Object: "open", Confidence: 80,
	}}
	if _, err := restored.Observe(newFact); err != nil {
		t.Fatal(err)
	}
	state, err := restored.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actors["npc.mira"]
	if selected := actor.Beliefs["gate:state"]; selected.SourceEventID != newFact.EventID {
		t.Fatalf("old-chain revision defeated deterministic same-tick fact tie-break: %+v", selected)
	}
	set := actor.BeliefSets["gate:state"]
	revisions := make(map[string]uint64, len(set.Claims))
	for _, claim := range set.Claims {
		revisions[claim.Fact.SourceEventID] = claim.ObservedRevision
	}
	if revisions[oldFact.EventID] != 1 || revisions[newFact.EventID] != 2 {
		t.Fatalf("belief revisions were not rebased into the new chain: %+v", revisions)
	}
	oldIndex, newIndex := -1, -1
	for index, memory := range actor.Memories {
		switch memory.EventID {
		case oldFact.EventID:
			oldIndex = index
			if memory.CreatedRevision != 1 {
				t.Fatalf("old memory revision = %d, want restore revision 1", memory.CreatedRevision)
			}
		case newFact.EventID:
			newIndex = index
			if memory.CreatedRevision != 2 {
				t.Fatalf("new memory revision = %d, want 2", memory.CreatedRevision)
			}
		}
	}
	if oldIndex < 0 || newIndex < 0 || oldIndex >= newIndex {
		t.Fatalf("same-tick memories are not ordered by the new chain: old=%d new=%d", oldIndex, newIndex)
	}
	finalSnapshot, err := restored.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if err := rinruntime.ValidateSnapshot(finalSnapshot); err != nil {
		t.Fatalf("rebased restore state must remain snapshot-compatible: %v", err)
	}
}

func TestMemoryIsBounded(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.memory"))
	for index := 1; index <= 130; index++ {
		request := observeRequest("session.memory", fmt.Sprintf("observe.%d", index), fmt.Sprintf("event.%d", index), int64(index))
		if _, err := engine.Observe(request); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engine.State(sessionRequest("session.memory"))
	if err != nil {
		t.Fatal(err)
	}
	memories := state.Actors["npc.mira"].Memories
	if len(memories) != 128 || memories[0].EventID != "event.3" || memories[127].EventID != "event.130" {
		t.Fatalf("unexpected bounded memory range: first=%s last=%s count=%d", memories[0].EventID, memories[len(memories)-1].EventID, len(memories))
	}
}

func TestPendingProposalCapacityFailsClosedAndSnapshotRemainsValid(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	const sessionID = "session.pending-proposal-capacity"
	create := createRequest(sessionID)
	create.Features = append(create.Features, protocol.FeatureArbitration)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 64; index++ {
		request := proposeRequest(
			sessionID,
			fmt.Sprintf("propose.pending-capacity.%02d", index),
			0,
			nil,
		)
		if _, _, err := engine.Propose(context.Background(), request); err != nil {
			t.Fatalf("proposal %d: %v", index, err)
		}
	}
	overflow := proposeRequest(sessionID, "propose.pending-capacity.overflow", 0, nil)
	if _, _, err := engine.Propose(context.Background(), overflow); !errors.Is(err, rinruntime.ErrConflict) ||
		rinruntime.ErrorCode(err) != "proposal_capacity" {
		t.Fatalf("65th pending proposal should fail closed: %v", err)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Proposals) != 64 {
		t.Fatalf("pending proposal count = %d, want 64", len(state.Proposals))
	}
	snapshot, err := engine.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if err := rinruntime.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("capacity-bounded proposal state must remain restorable: %v", err)
	}
}

type invalidPolicy struct{}

func (invalidPolicy) Propose(context.Context, rinruntime.PolicyContext) (rinruntime.ProposalDraft, error) {
	return rinruntime.ProposalDraft{ActionID: "execute.arbitrary", Stance: "engage", Summary: "bad", Rationale: "bad"}, nil
}

func TestPolicyCannotEscapeCandidateWhitelist(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), invalidPolicy{})
	_, _ = engine.CreateSession(createRequest("session.whitelist"))
	_, _, err := engine.Propose(context.Background(), proposeRequest("session.whitelist", "propose.bad", 0, nil))
	if rinruntime.ErrorCode(err) != "invalid_policy_output" {
		t.Fatalf("expected invalid policy output, got %v", err)
	}
}

type blockingPolicy struct {
	started chan struct{}
	release chan struct{}
}

func (p blockingPolicy) Propose(ctx context.Context, input rinruntime.PolicyContext) (rinruntime.ProposalDraft, error) {
	close(p.started)
	select {
	case <-p.release:
		return rinruntime.ProposalDraft{ActionID: "talk", Stance: "engage", Summary: "Mira proposes a reply.", Rationale: "Allowed by the game."}, nil
	case <-ctx.Done():
		return rinruntime.ProposalDraft{}, ctx.Err()
	}
}

type firstCallBlockingPolicy struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (p *firstCallBlockingPolicy) Propose(ctx context.Context, input rinruntime.PolicyContext) (rinruntime.ProposalDraft, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return rinruntime.ProposalDraft{}, ctx.Err()
		}
	}
	return rinruntime.ProposalDraft{
		ActionID:  "talk",
		Stance:    "engage",
		Summary:   "Mira proposes a reply.",
		Rationale: "Allowed by the game.",
	}, nil
}

func TestConcurrentIdempotentProposeReturnsOriginalEvictedProposal(t *testing.T) {
	const sessionID = "session.concurrent-evicted-proposal"
	policy := &firstCallBlockingPolicy{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-policy.release:
		default:
			close(policy.release)
		}
	}()
	engine := newEngine(t, store.NewMemory(), policy)
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	request := proposeRequest(sessionID, "propose.concurrent-evicted", 0, nil)
	type proposalCallResult struct {
		proposal  protocol.ActionProposal
		duplicate bool
		err       error
	}
	firstResult := make(chan proposalCallResult, 1)
	go func() {
		result, duplicate, err := engine.Propose(context.Background(), request)
		firstResult <- proposalCallResult{proposal: result, duplicate: duplicate, err: err}
	}()
	select {
	case <-policy.started:
	case <-time.After(time.Second):
		t.Fatal("first policy call did not start")
	}

	proposal, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("second call unexpectedly reported a duplicate")
	}
	if _, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.concurrent-evicted",
		ProposalID:      proposal.ID,
		EventID:         "event.concurrent-evicted",
		Tick:            0,
		Accepted:        true,
		Outcome:         "The game applied the reply.",
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 64; index++ {
		next := proposeRequest(
			sessionID,
			fmt.Sprintf("propose.after-eviction.%02d", index),
			5,
			nil,
		)
		if _, _, err := engine.Propose(context.Background(), next); err != nil {
			t.Fatalf("retained proposal %d: %v", index, err)
		}
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Proposals[proposal.ID]; exists {
		t.Fatal("resolved proposal was not evicted before the blocked retry resumed")
	}

	close(policy.release)
	select {
	case result := <-firstResult:
		if result.err != nil || !result.duplicate || !reflect.DeepEqual(result.proposal, proposal) {
			t.Fatalf("blocked idempotent call did not return its original proposal: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked idempotent call did not return")
	}
}

func TestPolicyWaitDoesNotBlockObservations(t *testing.T) {
	policy := blockingPolicy{started: make(chan struct{}), release: make(chan struct{})}
	engine := newEngine(t, store.NewMemory(), policy)
	_, _ = engine.CreateSession(createRequest("session.concurrent"))
	result := make(chan error, 1)
	go func() {
		_, _, err := engine.Propose(context.Background(), proposeRequest("session.concurrent", "propose.concurrent", 0, nil))
		result <- err
	}()
	select {
	case <-policy.started:
	case <-time.After(time.Second):
		t.Fatal("policy did not start")
	}
	observed := make(chan error, 1)
	go func() {
		_, err := engine.Observe(observeRequest("session.concurrent", "observe.concurrent", "event.concurrent", 0))
		observed <- err
	}()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observation was blocked by policy execution")
	}
	close(policy.release)
	if err := <-result; !errors.Is(err, rinruntime.ErrStale) {
		t.Fatalf("proposal should become stale after concurrent observation, got %v", err)
	}
}

func newEngine(t *testing.T, eventStore rinruntime.Store, selectedPolicy rinruntime.Policy) *rinruntime.Engine {
	t.Helper()
	engine, err := rinruntime.Open(eventStore, selectedPolicy)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func createRequest(sessionID string) protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + sessionID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID:         "game.demo",
			ContentID:      "content.base",
			ContentVersion: "1.0.0",
			ContentHash:    "sha256-demo",
		},
		Seed: 42,
		Features: []string{
			protocol.FeatureOutcomeReporting,
		},
		Actors: []protocol.ActorSeed{{
			ID:          "npc.mira",
			Kind:        "npc",
			DisplayName: "Mira",
			Traits:      []string{"curious", "careful"},
			Boundaries: []protocol.Boundary{{
				ID:          "boundary.privacy",
				Description: "Do not disclose private correspondence.",
				TriggerTags: []string{"private"},
				Response:    "refuse",
			}},
			Goals: []protocol.Goal{{
				ID:               "goal.connect",
				Description:      "Build trust through specific actions.",
				Priority:         4,
				PreferredActions: []string{"talk"},
				TargetProgress:   3,
				Status:           "active",
			}},
			ThinkEveryTicks: 5,
			Enabled:         true,
		}},
	}
}

func observeRequest(sessionID, requestID, eventID string, tick int64) protocol.ObserveRequest {
	return protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		EventID:         eventID,
		Tick:            tick,
		ObserverIDs:     []string{"npc.mira"},
		Source:          "game",
		Kind:            "dialogue",
		Summary:         "The player waited instead of demanding an answer.",
		Quote:           "Take your time.",
		Tags:            []string{"conversation", "trust"},
		Importance:      4,
	}
}

func proposeRequest(sessionID, requestID string, tick int64, tags []string) protocol.ProposeRequest {
	return protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		ActorID:         "npc.mira",
		Tick:            tick,
		Intent:          "Choose how to respond to the player.",
		Tags:            tags,
		CandidateActions: []protocol.ActionSpec{
			{ID: "talk", Kind: "dialogue", Description: "ask one honest question"},
			{ID: "refuse", Kind: "refuse", Description: "protect a private boundary"},
			{ID: "wait", Kind: "wait", Description: "stay silent for now"},
		},
	}
}

func sessionRequest(sessionID string) protocol.SessionRequest {
	return protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID}
}

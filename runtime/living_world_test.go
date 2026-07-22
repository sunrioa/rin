package runtime_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestCandidateGoalIsAdoptedOnlyAfterAcceptedCommit(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	create := createRequest("session.goals")
	create.Features = []string{protocol.FeatureGoalCandidates}
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	candidate := protocol.Goal{
		ID: "goal.restore-camera", Description: "Restore the damaged camera.", Motivation: "Recover a shared creative tool.",
		Priority: 5, PreferredActions: []string{"talk"}, TargetProgress: 3, Status: "active",
	}
	request := proposeRequest("session.goals", "propose.goal-rejected", 0, nil)
	request.CandidateGoals = []protocol.Goal{candidate}
	rejected, _, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.GoalID != candidate.ID || rejected.ProposedGoal == nil {
		t.Fatalf("candidate goal was not represented in the proposal: %+v", rejected)
	}
	if _, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "commit.goal-rejected",
		ProposalID: rejected.ID, EventID: "event.goal-rejected", Tick: 0, Accepted: false,
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := engine.State(sessionRequest(create.SessionID))
	if goalInState(state.Actors["npc.mira"], candidate.ID) {
		t.Fatal("rejected candidate goal entered actor state")
	}

	request.RequestID = "propose.goal-accepted"
	accepted, _, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "commit.goal-accepted",
		ProposalID: accepted.ID, EventID: "event.goal-accepted", Tick: 0, Accepted: true,
		Outcome: "Mira decided to ask how they could repair the camera together.",
	}); err != nil {
		t.Fatal(err)
	}
	state, _ = engine.State(sessionRequest(create.SessionID))
	goal, found := findGoal(state.Actors["npc.mira"], candidate.ID)
	if !found || goal.Progress != 1 {
		t.Fatalf("accepted candidate goal was not adopted and advanced: %+v", state.Actors["npc.mira"].Goals)
	}
}

func TestDormantActorIsExcludedUntilGameWakesIt(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	create := createRequest("session.activity")
	create.Features = []string{protocol.FeatureActorActivity}
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SetActorActivity(protocol.SetActorActivityRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "activity.sleep", Tick: 1,
		Updates: []protocol.ActorActivityUpdate{{ActorID: "npc.mira", RegionID: "region.harbor", State: "dormant", Reason: "region unloaded"}},
	}); err != nil {
		t.Fatal(err)
	}
	due, err := engine.DueAgents(protocol.DueAgentsRequest{ProtocolVersion: protocol.Version, SessionID: create.SessionID, Tick: 10, Limit: 10})
	if err != nil || len(due.Agents) != 0 {
		t.Fatalf("dormant actor should not be due: %+v err=%v", due, err)
	}
	_, _, err = engine.Propose(context.Background(), proposeRequest(create.SessionID, "propose.sleeping", 10, nil))
	if !errors.Is(err, rinruntime.ErrNotDue) || rinruntime.ErrorCode(err) != "actor_dormant" {
		t.Fatalf("dormant actor should not propose: %v", err)
	}
	if _, err := engine.SetActorActivity(protocol.SetActorActivityRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "activity.wake", Tick: 2,
		Updates: []protocol.ActorActivityUpdate{{ActorID: "npc.mira", RegionID: "region.harbor", State: "awake", Reason: "region loaded"}},
	}); err != nil {
		t.Fatal(err)
	}
	due, err = engine.DueAgents(protocol.DueAgentsRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, Tick: 10, Limit: 10, RegionIDs: []string{"region.market"},
	})
	if err != nil || len(due.Agents) != 0 {
		t.Fatalf("region filter should exclude actor: %+v err=%v", due, err)
	}
	due, err = engine.DueAgents(protocol.DueAgentsRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, Tick: 10, Limit: 10, RegionIDs: []string{"region.harbor"},
	})
	if err != nil || len(due.Agents) != 1 || due.Agents[0].RegionID != "region.harbor" {
		t.Fatalf("awake actor should be due in its region: %+v err=%v", due, err)
	}
}

func TestArbitrationIsDeterministicAndBatchCommitIsAtomic(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	create := twoActorWorldRequest("session.arbitration")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	miraRequest := targetedProposalRequest(create.SessionID, "propose.mira", "npc.mira")
	mira, _, err := engine.Propose(context.Background(), miraRequest)
	if err != nil {
		t.Fatal(err)
	}
	orenRequest := targetedProposalRequest(create.SessionID, "propose.oren", "npc.oren")
	oren, _, err := engine.Propose(context.Background(), orenRequest)
	if err != nil {
		t.Fatalf("another proposal should not change world revision: %v", err)
	}
	if mira.BasedOnWorldRevision != 1 || oren.BasedOnWorldRevision != 1 {
		t.Fatalf("proposals should share world revision: mira=%d oren=%d", mira.BasedOnWorldRevision, oren.BasedOnWorldRevision)
	}

	first, _, err := engine.Arbitrate(protocol.ArbitrateRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "arbitrate.first", Tick: 0,
		ProposalIDs: []string{oren.ID, mira.ID}, ExclusiveTargetIDs: []string{"object.camera"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := engine.Arbitrate(protocol.ArbitrateRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "arbitrate.second", Tick: 0,
		ProposalIDs: []string{mira.ID, oren.ID}, ExclusiveTargetIDs: []string{"object.camera"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Decisions, second.Decisions) {
		t.Fatalf("arbitration depended on input order: first=%+v second=%+v", first.Decisions, second.Decisions)
	}
	if len(first.Decisions) != 2 || first.Decisions[0].ProposalID != mira.ID || first.Decisions[0].Status != "selected" || first.Decisions[1].Status != "deferred" {
		t.Fatalf("unexpected arbitration decisions: %+v", first.Decisions)
	}

	result, err := engine.CommitBatch(protocol.BatchCommitRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "commit.batch", Tick: 0,
		Items: []protocol.CommitItem{
			{ProposalID: mira.ID, EventID: "event.mira", Accepted: true, Outcome: "Mira reached the camera first."},
			{ProposalID: oren.ID, EventID: "event.oren", Accepted: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 6 {
		t.Fatalf("expected one atomic batch event at revision 6, got %+v", result)
	}
	state, _ := engine.State(sessionRequest(create.SessionID))
	if state.WorldRevision != 2 || state.Proposals[mira.ID].Status != "accepted" || state.Proposals[oren.ID].Status != "rejected" {
		t.Fatalf("unexpected post-batch state: %+v", state)
	}
	if _, err := engine.Snapshot(sessionRequest(create.SessionID)); err != nil {
		t.Fatalf("coordinated world snapshot should validate: %v", err)
	}
}

func TestBatchCommitRejectsStaleWorldWithoutPartialMutation(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	create := twoActorWorldRequest("session.batch-stale")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.before-change", "npc.mira"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(observeRequest(create.SessionID, "observe.change", "event.change", 0)); err != nil {
		t.Fatal(err)
	}
	_, err = engine.CommitBatch(protocol.BatchCommitRequest{
		ProtocolVersion: protocol.Version, SessionID: create.SessionID, RequestID: "commit.stale-batch", Tick: 0,
		Items: []protocol.CommitItem{{ProposalID: proposal.ID, EventID: "event.should-not-commit", Accepted: true, Outcome: "Should not happen."}},
	})
	if !errors.Is(err, rinruntime.ErrStale) {
		t.Fatalf("expected stale batch rejection, got %v", err)
	}
	state, _ := engine.State(sessionRequest(create.SessionID))
	if state.Proposals[proposal.ID].Status != "pending" || len(state.Actors["npc.mira"].RecentActions) != 0 {
		t.Fatalf("failed batch partially mutated state: %+v", state)
	}
}

func twoActorWorldRequest(sessionID string) protocol.CreateSessionRequest {
	create := createRequest(sessionID)
	create.Features = []string{protocol.FeatureArbitration}
	oren := create.Actors[0]
	oren.ID = "npc.oren"
	oren.DisplayName = "Oren"
	oren.Goals = []protocol.Goal{{
		ID: "goal.document", Description: "Document the damaged camera.", Priority: 2,
		PreferredActions: []string{"talk"}, TargetProgress: 3, Status: "active",
	}}
	create.Actors = append(create.Actors, oren)
	return create
}

func targetedProposalRequest(sessionID, requestID, actorID string) protocol.ProposeRequest {
	request := proposeRequest(sessionID, requestID, 0, nil)
	request.ActorID = actorID
	request.CandidateActions = []protocol.ActionSpec{
		{ID: "talk", Kind: "dialogue", Description: "inspect the camera", TargetIDs: []string{"object.camera"}},
		{ID: "wait", Kind: "wait", Description: "wait"},
	}
	return request
}

func goalInState(actor protocol.ActorState, goalID string) bool {
	_, found := findGoal(actor, goalID)
	return found
}

func findGoal(actor protocol.ActorState, goalID string) (protocol.Goal, bool) {
	for _, goal := range actor.Goals {
		if goal.ID == goalID {
			return goal, true
		}
	}
	return protocol.Goal{}, false
}

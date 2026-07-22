package runtime_test

import (
	"context"
	"errors"
	"fmt"
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
	if !repeated.Duplicate || repeated.Revision != 4 {
		t.Fatalf("expected idempotent result, got %+v", repeated)
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

func TestAcceptedProposalBecomesStaleAfterObservation(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.stale"))
	proposal, _, err := engine.Propose(context.Background(), proposeRequest("session.stale", "propose.stale", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Observe(observeRequest("session.stale", "observe.after", "event.after", 0))
	if err != nil {
		t.Fatal(err)
	}
	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.stale",
		RequestID:       "commit.stale",
		ProposalID:      proposal.ID,
		EventID:         "event.commit.stale",
		Tick:            0,
		Accepted:        true,
		Outcome:         "Should not happen.",
	}
	_, err = engine.Commit(commit)
	if !errors.Is(err, rinruntime.ErrStale) {
		t.Fatalf("expected stale proposal, got %v", err)
	}
	commit.RequestID = "commit.reject"
	commit.EventID = "event.commit.reject"
	commit.Accepted = false
	commit.Outcome = ""
	if _, err := engine.Commit(commit); err != nil {
		t.Fatalf("stale proposal should remain rejectable: %v", err)
	}
}

func TestSnapshotTamperAndFreshRestore(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	_, _ = engine.CreateSession(createRequest("session.snapshot"))
	_, _ = engine.Observe(observeRequest("session.snapshot", "observe.snapshot", "event.snapshot", 4))
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
	invalidSnapshot, err := rinruntime.SnapshotOf(invalidState)
	if err != nil {
		t.Fatal(err)
	}
	if err := rinruntime.ValidateSnapshot(invalidSnapshot); err == nil {
		t.Fatal("internally hashed but structurally invalid snapshot should be rejected")
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

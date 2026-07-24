package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	goruntime "runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type invariantStore struct {
	mu          sync.Mutex
	events      map[string][]protocol.EventRecord
	appendCalls int
	saveCalls   int
}

func newInvariantStore() *invariantStore {
	return &invariantStore{events: make(map[string][]protocol.EventRecord)}
}

func (s *invariantStore) Create(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[sessionID]; exists {
		return ErrConflict
	}
	event.Data = append([]byte(nil), event.Data...)
	s.events[sessionID] = []protocol.EventRecord{event}
	return nil
}

func (s *invariantStore) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[sessionID]; !exists {
		return ErrNotFound
	}
	s.appendCalls++
	event.Data = append([]byte(nil), event.Data...)
	s.events[sessionID] = append(s.events[sessionID], event)
	return nil
}

func (s *invariantStore) Load(sessionID string) ([]protocol.EventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, exists := s.events[sessionID]
	if !exists {
		return nil, ErrNotFound
	}
	result := make([]protocol.EventRecord, len(events))
	for index, event := range events {
		event.Data = append([]byte(nil), event.Data...)
		result[index] = event
	}
	return result, nil
}

func (s *invariantStore) ListSessions() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.events))
	for id := range s.events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *invariantStore) SaveSnapshot(string, protocol.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	return nil
}

func (s *invariantStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendCalls, s.saveCalls
}

type invariantPolicy struct {
	propose func(context.Context, PolicyContext) (ProposalDraft, error)
}

func (p invariantPolicy) Propose(ctx context.Context, input PolicyContext) (ProposalDraft, error) {
	if p.propose != nil {
		return p.propose(ctx, input)
	}
	draft := ProposalDraft{
		ActionID:     input.Request.CandidateActions[0].ID,
		Stance:       "wait",
		Summary:      "Wait and observe.",
		Rationale:    "The test policy selects a deterministic candidate.",
		PolicySource: "test",
	}
	if len(input.Request.CandidateGoals) > 0 {
		draft.GoalID = input.Request.CandidateGoals[0].ID
	}
	return draft, nil
}

func invariantCreate(sessionID string, features []string, goals []protocol.Goal) protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + sessionID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID:         "game.invariants",
			ContentID:      "content.invariants",
			ContentVersion: "1",
			ContentHash:    "sha256-invariants",
		},
		Features: append([]string(nil), features...),
		Actors: []protocol.ActorSeed{{
			ID:              "npc.mira",
			Kind:            "npc",
			DisplayName:     "Mira",
			Metadata:        map[string]string{"origin": "live"},
			Goals:           append([]protocol.Goal(nil), goals...),
			ThinkEveryTicks: 5,
			Enabled:         true,
		}},
	}
}

func invariantEngine(
	t *testing.T,
	sessionID string,
	features []string,
	goals []protocol.Goal,
	selectedPolicy Policy,
) (*Engine, *invariantStore) {
	t.Helper()
	eventStore := newInvariantStore()
	engine, err := Open(eventStore, selectedPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CreateSession(invariantCreate(sessionID, features, goals)); err != nil {
		t.Fatal(err)
	}
	return engine, eventStore
}

func invariantObserve(sessionID, requestID, eventID string, tick int64) protocol.ObserveRequest {
	return protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		EventID:         eventID,
		Tick:            tick,
		ObserverIDs:     []string{"npc.mira"},
		Source:          "game",
		Kind:            "test",
		Summary:         "An invariant test observation.",
		Importance:      1,
	}
}

func invariantPropose(sessionID, requestID string, candidateGoals []protocol.Goal) protocol.ProposeRequest {
	return protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		ActorID:         "npc.mira",
		Tick:            0,
		Intent:          "Choose a test action.",
		CandidateActions: []protocol.ActionSpec{{
			ID:          "wait",
			Kind:        "wait",
			Description: "Wait for more information.",
			Parameters:  map[string]string{"duration": "short"},
		}},
		CandidateGoals: append([]protocol.Goal(nil), candidateGoals...),
	}
}

func invariantGoal(id string) protocol.Goal {
	return protocol.Goal{
		ID:             id,
		Description:    "A bounded invariant-test goal.",
		Priority:       1,
		TargetProgress: 10,
		Status:         "active",
	}
}

func TestSnapshotOfValidatesBeforeHashingAndSaving(t *testing.T) {
	const sessionID = "session.snapshot-invariants"
	engine, eventStore := invariantEngine(t, sessionID, nil, nil, invariantPolicy{})
	state, err := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotOf(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("successful SnapshotOf result must validate: %v", err)
	}

	session := engine.sessions[sessionID]
	session.mu.Lock()
	session.state.Tick = -1
	session.mu.Unlock()
	if _, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	}); ErrorCode(err) != "snapshot_failed" {
		t.Fatalf("invalid live state should fail snapshot creation, got %v", err)
	}
	_, saveCalls := eventStore.counts()
	if saveCalls != 0 {
		t.Fatalf("invalid snapshot reached Store.SaveSnapshot %d times", saveCalls)
	}
}

func TestRevisionOverflowDoesNotWrapOrAppend(t *testing.T) {
	const sessionID = "session.revision-overflow"
	engine, eventStore := invariantEngine(t, sessionID, nil, nil, invariantPolicy{})
	session := engine.sessions[sessionID]
	session.mu.Lock()
	session.state.Revision = ^uint64(0)
	overflowState := session.state
	session.mu.Unlock()

	request := invariantObserve(sessionID, "observe.overflow", "event.overflow", 0)
	if _, err := engine.Observe(request); ErrorCode(err) != "revision_overflow" ||
		!errors.Is(err, ErrConflict) {
		t.Fatalf("revision overflow should be explicit, got %v", err)
	}
	appendCalls, _ := eventStore.counts()
	if appendCalls != 0 {
		t.Fatalf("revision overflow reached Store.Append %d times", appendCalls)
	}
	if _, err := newEvent(overflowState, EventObserved, request.RequestID, observedPayload{Request: request}, time.Now()); ErrorCode(err) != "revision_overflow" {
		t.Fatalf("newEvent should reject an exhausted revision, got %v", err)
	}
	if err := verifyEvent(overflowState, protocol.EventRecord{}); !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("verifyEvent should reject a successor after MaxUint64, got %v", err)
	}
}

func TestRevisionOverflowSkipsProposalPolicy(t *testing.T) {
	const sessionID = "session.proposal-revision-overflow"
	policyCalled := false
	selectedPolicy := invariantPolicy{propose: func(context.Context, PolicyContext) (ProposalDraft, error) {
		policyCalled = true
		return ProposalDraft{}, errors.New("policy must not be called")
	}}
	engine, eventStore := invariantEngine(t, sessionID, nil, nil, selectedPolicy)
	session := engine.sessions[sessionID]
	session.mu.Lock()
	session.state.Revision = ^uint64(0)
	session.mu.Unlock()

	if _, _, err := engine.Propose(
		context.Background(),
		invariantPropose(sessionID, "propose.revision-overflow", nil),
	); ErrorCode(err) != "revision_overflow" || ErrorField(err) != "revision" {
		t.Fatalf("proposal revision overflow should be explicit, got %v", err)
	}
	if policyCalled {
		t.Fatal("revision exhaustion called the external policy")
	}
	appendCalls, _ := eventStore.counts()
	if appendCalls != 0 {
		t.Fatalf("proposal revision overflow reached Store.Append %d times", appendCalls)
	}
}

func TestWorldRevisionOverflowIsExplicitBeforeAppend(t *testing.T) {
	assertOverflow := func(
		t *testing.T,
		engine *Engine,
		eventStore *invariantStore,
		sessionID string,
		mutate func() error,
	) {
		t.Helper()
		session := engine.sessions[sessionID]
		session.mu.Lock()
		session.state.WorldRevision = ^uint64(0)
		session.mu.Unlock()
		before := mustEngineState(t, engine, sessionID)
		beforeAppends, _ := eventStore.counts()

		err := mutate()
		if ErrorCode(err) != "world_revision_overflow" ||
			ErrorField(err) != "world_revision" ||
			!errors.Is(err, ErrConflict) {
			t.Fatalf("world revision overflow should be explicit, got %v", err)
		}
		afterAppends, _ := eventStore.counts()
		if afterAppends != beforeAppends {
			t.Fatalf("world revision overflow changed append count from %d to %d", beforeAppends, afterAppends)
		}
		after := mustEngineState(t, engine, sessionID)
		if !reflect.DeepEqual(after, before) {
			t.Fatal("world revision overflow changed live state")
		}
	}

	t.Run("observe", func(t *testing.T) {
		const sessionID = "session.world-overflow-observe"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureArbitration},
			nil,
			invariantPolicy{},
		)
		assertOverflow(t, engine, eventStore, sessionID, func() error {
			_, err := engine.Observe(invariantObserve(
				sessionID,
				"observe.world-overflow",
				"event.world-overflow",
				0,
			))
			return err
		})
	})

	t.Run("commit", func(t *testing.T) {
		const sessionID = "session.world-overflow-commit"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureArbitration, protocol.FeatureOutcomeReporting},
			nil,
			invariantPolicy{},
		)
		proposal, _, err := engine.Propose(
			context.Background(),
			invariantPropose(sessionID, "propose.world-overflow", nil),
		)
		if err != nil {
			t.Fatal(err)
		}
		assertOverflow(t, engine, eventStore, sessionID, func() error {
			_, err := engine.Commit(protocol.CommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "commit.world-overflow",
				ProposalID:      proposal.ID,
				EventID:         "event.world-overflow",
				Accepted:        true,
				Outcome:         "The action cannot advance an exhausted world revision.",
			})
			return err
		})
	})

	t.Run("batch", func(t *testing.T) {
		const sessionID = "session.world-overflow-batch"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureArbitration, protocol.FeatureOutcomeReporting},
			nil,
			invariantPolicy{},
		)
		proposal, _, err := engine.Propose(
			context.Background(),
			invariantPropose(sessionID, "propose.world-overflow", nil),
		)
		if err != nil {
			t.Fatal(err)
		}
		assertOverflow(t, engine, eventStore, sessionID, func() error {
			_, err := engine.CommitBatch(protocol.BatchCommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "batch.world-overflow",
				Items: []protocol.CommitItem{{
					ProposalID: proposal.ID,
					EventID:    "event.world-overflow",
					Accepted:   true,
					Outcome:    "The batch cannot advance an exhausted world revision.",
				}},
			})
			return err
		})
	})

	t.Run("activity", func(t *testing.T) {
		const sessionID = "session.world-overflow-activity"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureArbitration, protocol.FeatureActorActivity},
			nil,
			invariantPolicy{},
		)
		assertOverflow(t, engine, eventStore, sessionID, func() error {
			_, err := engine.SetActorActivity(protocol.SetActorActivityRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "activity.world-overflow",
				Updates: []protocol.ActorActivityUpdate{{
					ActorID: "npc.mira",
					State:   "awake",
				}},
			})
			return err
		})
	})
}

func TestPolicyContextMutationCannotReachLiveStateOrCaller(t *testing.T) {
	const sessionID = "session.policy-isolation"
	var actorCameFromStateCopy bool
	injected := errors.New("injected policy failure")
	selectedPolicy := invariantPolicy{propose: func(_ context.Context, input PolicyContext) (ProposalDraft, error) {
		input.Actor.Metadata["policy"] = "mutated"
		input.Actor.Goals[0].Description = "mutated through actor context"
		actorCameFromStateCopy = input.State.Actors[input.Actor.ID].Metadata["policy"] == "mutated"
		stateActor := input.State.Actors[input.Actor.ID]
		stateActor.DisplayName = "Mutated State"
		input.State.Actors[input.Actor.ID] = stateActor
		input.Request.Tags[0] = "mutated"
		input.Request.CandidateActions[0].Parameters["duration"] = "mutated"
		return ProposalDraft{}, injected
	}}
	engine, _ := invariantEngine(
		t,
		sessionID,
		nil,
		[]protocol.Goal{invariantGoal("goal.existing")},
		selectedPolicy,
	)
	baseline, err := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	request := invariantPropose(sessionID, "propose.isolation", nil)
	request.Tags = []string{"original"}
	originalRequest, err := clone(request)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := engine.Propose(context.Background(), request); ErrorCode(err) != "policy_failed" ||
		!errors.Is(err, injected) {
		t.Fatalf("injected policy failure was not preserved: %v", err)
	}
	if !actorCameFromStateCopy {
		t.Fatal("policy Actor did not share the isolated State actor backing data")
	}
	after, err := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, baseline) {
		t.Fatalf("policy mutation escaped into live state:\nbefore=%+v\nafter=%+v", baseline, after)
	}
	if !reflect.DeepEqual(request, originalRequest) {
		t.Fatalf("policy mutation escaped into caller request:\nbefore=%+v\nafter=%+v", originalRequest, request)
	}
}

func TestPolicyContextMutationIsRaceIsolated(t *testing.T) {
	const sessionID = "session.policy-race-isolation"
	started := make(chan struct{})
	release := make(chan struct{})
	selectedPolicy := invariantPolicy{propose: func(_ context.Context, input PolicyContext) (ProposalDraft, error) {
		close(started)
		<-release
		for index := 0; index < 2_000; index++ {
			input.Actor.Goals[0].Description = fmt.Sprintf("policy mutation %d", index)
			input.Request.CandidateActions[0].Description = fmt.Sprintf("request mutation %d", index)
			goruntime.Gosched()
		}
		return ProposalDraft{
			ActionID:     input.Request.CandidateActions[0].ID,
			GoalID:       "goal.existing",
			Stance:       "wait",
			Summary:      "Wait after the concurrent update.",
			Rationale:    "Exercise successful draft validation against an isolated actor generation.",
			PolicySource: "test",
		}, nil
	}}
	engine, _ := invariantEngine(
		t,
		sessionID,
		nil,
		[]protocol.Goal{invariantGoal("goal.existing")},
		selectedPolicy,
	)
	request := invariantPropose(sessionID, "propose.race-isolation", nil)
	originalDescription := request.CandidateActions[0].Description
	result := make(chan error, 1)
	go func() {
		_, _, err := engine.Propose(context.Background(), request)
		result <- err
	}()
	<-started
	if _, err := engine.Observe(invariantObserve(
		sessionID,
		"observe.concurrent-policy",
		"event.concurrent-policy",
		0,
	)); err != nil {
		t.Fatal(err)
	}
	close(release)
	for index := 0; index < 200; index++ {
		state, err := engine.State(protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if state.Actors["npc.mira"].Goals[0].Description != invariantGoal("unused").Description {
			t.Fatal("concurrent policy mutation escaped into live state")
		}
		if request.CandidateActions[0].Description != originalDescription {
			t.Fatal("concurrent policy mutation escaped into caller request")
		}
		goruntime.Gosched()
	}
	if err := <-result; ErrorCode(err) != "state_changed" || !errors.Is(err, ErrStale) {
		t.Fatalf("unexpected concurrent policy result: %v", err)
	}
}

func TestFactVisibilityRejectsUnknownActorsBeforeAppend(t *testing.T) {
	unknownFact := protocol.Fact{
		SubjectID: "relic", Predicate: "location", Object: "tower",
		Visibility: []string{"npc.mira", "npc.ghost"}, Confidence: 80,
	}

	t.Run("observe", func(t *testing.T) {
		const sessionID = "session.visibility-observe"
		engine, eventStore := invariantEngine(t, sessionID, nil, nil, invariantPolicy{})
		request := invariantObserve(sessionID, "observe.visibility", "event.visibility", 1)
		request.Facts = []protocol.Fact{unknownFact}
		if _, err := engine.Observe(request); ErrorCode(err) != "unknown_actor" ||
			ErrorField(err) != "facts[0].visibility[1]" {
			t.Fatalf("unknown visibility actor should fail precisely, got %v", err)
		}
		appendCalls, _ := eventStore.counts()
		if appendCalls != 0 {
			t.Fatalf("invalid observation reached Store.Append %d times", appendCalls)
		}
	})

	t.Run("commit", func(t *testing.T) {
		const sessionID = "session.visibility-commit"
		engine, eventStore := invariantEngine(t, sessionID, nil, nil, invariantPolicy{})
		proposal, _, err := engine.Propose(context.Background(), invariantPropose(sessionID, "propose.visibility", nil))
		if err != nil {
			t.Fatal(err)
		}
		before, _ := eventStore.counts()
		_, err = engine.Commit(protocol.CommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "commit.visibility",
			ProposalID:      proposal.ID,
			EventID:         "event.visibility",
			Accepted:        true,
			Outcome:         "The actor waited.",
			Facts:           []protocol.Fact{unknownFact},
		})
		if ErrorCode(err) != "unknown_actor" || ErrorField(err) != "facts[0].visibility[1]" {
			t.Fatalf("unknown commit visibility actor should fail precisely, got %v", err)
		}
		after, _ := eventStore.counts()
		if after != before {
			t.Fatalf("invalid commit changed append count from %d to %d", before, after)
		}
	})

	t.Run("batch", func(t *testing.T) {
		const sessionID = "session.visibility-batch"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureOutcomeReporting, protocol.FeatureArbitration},
			nil,
			invariantPolicy{},
		)
		proposal, _, err := engine.Propose(context.Background(), invariantPropose(sessionID, "propose.visibility", nil))
		if err != nil {
			t.Fatal(err)
		}
		before, _ := eventStore.counts()
		_, err = engine.CommitBatch(protocol.BatchCommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "batch.visibility",
			Items: []protocol.CommitItem{{
				ProposalID: proposal.ID,
				EventID:    "event.visibility",
				Accepted:   true,
				Outcome:    "The actor waited.",
				Facts:      []protocol.Fact{unknownFact},
			}},
		})
		if ErrorCode(err) != "unknown_actor" || ErrorField(err) != "items[0].facts[0].visibility[1]" {
			t.Fatalf("unknown batch visibility actor should fail precisely, got %v", err)
		}
		after, _ := eventStore.counts()
		if after != before {
			t.Fatalf("invalid batch changed append count from %d to %d", before, after)
		}
	})
}

func TestPendingProposedGoalsReserveActorCapacity(t *testing.T) {
	const sessionID = "session.goal-reservations"
	goals := make([]protocol.Goal, 31)
	for index := range goals {
		goals[index] = invariantGoal(fmt.Sprintf("goal.%02d", index))
	}
	engine, eventStore := invariantEngine(
		t,
		sessionID,
		[]string{
			protocol.FeatureOutcomeReporting,
			protocol.FeatureArbitration,
			protocol.FeatureGoalCandidates,
		},
		goals,
		invariantPolicy{},
	)
	reservedGoal := invariantGoal("goal.reserved")
	proposal, _, err := engine.Propose(
		context.Background(),
		invariantPropose(sessionID, "propose.reserved", []protocol.Goal{reservedGoal}),
	)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := eventStore.counts()

	sameGoal := invariantPropose(sessionID, "propose.same-reservation", []protocol.Goal{reservedGoal})
	if _, _, err := engine.Propose(context.Background(), sameGoal); ErrorCode(err) != "goal_exists" {
		t.Fatalf("duplicate pending goal reservation should fail, got %v", err)
	}
	differentGoal := invariantPropose(
		sessionID,
		"propose.over-capacity",
		[]protocol.Goal{invariantGoal("goal.over-capacity")},
	)
	if _, _, err := engine.Propose(context.Background(), differentGoal); ErrorCode(err) != "goal_capacity" {
		t.Fatalf("33rd committed-or-reserved goal should fail, got %v", err)
	}
	after, _ := eventStore.counts()
	if after != before {
		t.Fatalf("rejected goal proposals changed append count from %d to %d", before, after)
	}

	if _, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.reserved",
		ProposalID:      proposal.ID,
		EventID:         "event.reserved",
		Accepted:        true,
		Outcome:         "The reserved goal was accepted.",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.Actors["npc.mira"].Goals); got != 32 {
		t.Fatalf("accepted reserved goal count = %d, want 32", got)
	}
}

func TestCommitPathsDefendProposedGoalCapacity(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "commit"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			sessionID := "session.goal-defense-" + name
			goals := make([]protocol.Goal, 31)
			for index := range goals {
				goals[index] = invariantGoal(fmt.Sprintf("goal.%02d", index))
			}
			features := []string{protocol.FeatureOutcomeReporting, protocol.FeatureGoalCandidates}
			if batch {
				features = append(features, protocol.FeatureArbitration)
			}
			engine, eventStore := invariantEngine(t, sessionID, features, goals, invariantPolicy{})
			proposal, _, err := engine.Propose(
				context.Background(),
				invariantPropose(
					sessionID,
					"propose.capacity-defense",
					[]protocol.Goal{invariantGoal("goal.reserved")},
				),
			)
			if err != nil {
				t.Fatal(err)
			}

			session := engine.sessions[sessionID]
			session.mu.Lock()
			actor := session.state.Actors["npc.mira"]
			actor.Goals = append(actor.Goals, invariantGoal("goal.injected"))
			session.state.Actors[actor.ID] = actor
			session.mu.Unlock()
			before, _ := eventStore.counts()

			if batch {
				_, err = engine.CommitBatch(protocol.BatchCommitRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "batch.capacity-defense",
					Items: []protocol.CommitItem{{
						ProposalID: proposal.ID,
						EventID:    "event.capacity-defense",
						Accepted:   true,
						Outcome:    "Would exceed goal capacity.",
					}},
				})
			} else {
				_, err = engine.Commit(protocol.CommitRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "commit.capacity-defense",
					ProposalID:      proposal.ID,
					EventID:         "event.capacity-defense",
					Accepted:        true,
					Outcome:         "Would exceed goal capacity.",
				})
			}
			if ErrorCode(err) != "goal_capacity" {
				t.Fatalf("%s should reject a stale over-capacity reservation, got %v", name, err)
			}
			after, _ := eventStore.counts()
			if after != before {
				t.Fatalf("rejected %s changed append count from %d to %d", name, before, after)
			}
		})
	}
}

func TestEventIDExistsIncludesRetainedBeliefSources(t *testing.T) {
	for _, conflicts := range []bool{false, true} {
		name := "selected-belief"
		if conflicts {
			name = "nonselected-belief-claim"
		}
		t.Run(name, func(t *testing.T) {
			sessionID := "session.event-id-" + name
			features := []string{protocol.FeatureOutcomeReporting}
			if conflicts {
				features = append(features, protocol.FeatureBeliefConflicts)
			}
			engine, eventStore := invariantEngine(t, sessionID, features, nil, invariantPolicy{})
			first := invariantObserve(sessionID, "observe.1", "event.1", 1)
			first.Facts = []protocol.Fact{{
				SubjectID: "relic", Predicate: "location", Object: "harbor", Confidence: 80,
			}}
			if _, err := engine.Observe(first); err != nil {
				t.Fatal(err)
			}
			lastIndex := 129
			if conflicts {
				second := invariantObserve(sessionID, "observe.2", "event.2", 2)
				second.Facts = []protocol.Fact{{
					SubjectID: "relic", Predicate: "location", Object: "tower", Confidence: 90,
				}}
				if _, err := engine.Observe(second); err != nil {
					t.Fatal(err)
				}
				lastIndex = 130
			}
			start := 2
			if conflicts {
				start = 3
			}
			for index := start; index <= lastIndex; index++ {
				if _, err := engine.Observe(invariantObserve(
					sessionID,
					fmt.Sprintf("observe.%d", index),
					fmt.Sprintf("event.%d", index),
					int64(index),
				)); err != nil {
					t.Fatal(err)
				}
			}
			state, err := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID})
			if err != nil {
				t.Fatal(err)
			}
			for _, memory := range state.Actors["npc.mira"].Memories {
				if memory.EventID == "event.1" {
					t.Fatal("test setup did not evict event.1 from detailed memory")
				}
			}
			before, _ := eventStore.counts()
			reuse := invariantObserve(sessionID, "observe.reuse", "event.1", int64(lastIndex))
			if _, err := engine.Observe(reuse); ErrorCode(err) != "event_exists" {
				t.Fatalf("retained belief source event id should remain reserved, got %v", err)
			}
			after, _ := eventStore.counts()
			if after != before {
				t.Fatalf("duplicate belief source changed append count from %d to %d", before, after)
			}
		})
	}
}

func TestEventIDExistsIncludesRecentActionOutcomeAfterProposalAndMemoryEviction(t *testing.T) {
	const sessionID = "session.event-id-recent-action"
	engine, eventStore := invariantEngine(
		t,
		sessionID,
		[]string{protocol.FeatureOutcomeReporting, protocol.FeatureArbitration},
		nil,
		invariantPolicy{},
	)
	oldest, _, err := engine.Propose(
		context.Background(),
		invariantPropose(sessionID, "propose.oldest", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < maxProposals; index++ {
		proposal, _, err := engine.Propose(
			context.Background(),
			invariantPropose(sessionID, fmt.Sprintf("propose.rejected.%02d", index), nil),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Commit(protocol.CommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       fmt.Sprintf("commit.rejected.%02d", index),
			ProposalID:      proposal.ID,
			EventID:         fmt.Sprintf("event.rejected.%02d", index),
			Accepted:        false,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.Commit(protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.oldest",
		ProposalID:      oldest.ID,
		EventID:         "event.recent-action",
		Accepted:        true,
		Outcome:         "This memory will be evicted while the recent action remains.",
	}); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= maxMemories; index++ {
		if _, err := engine.Observe(invariantObserve(
			sessionID,
			fmt.Sprintf("observe.after-action.%03d", index),
			fmt.Sprintf("event.after-action.%03d", index),
			int64(index),
		)); err != nil {
			t.Fatal(err)
		}
	}
	next := invariantPropose(sessionID, "propose.trim-oldest", nil)
	next.Tick = maxMemories
	if _, _, err := engine.Propose(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	state, err := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, retained := state.Proposals[oldest.ID]; retained {
		t.Fatal("test setup did not evict the oldest resolved proposal")
	}
	for _, memory := range state.Actors["npc.mira"].Memories {
		if memory.EventID == "event.recent-action" {
			t.Fatal("test setup did not evict the recent action outcome memory")
		}
	}
	if len(state.Actors["npc.mira"].RecentActions) != 1 ||
		state.Actors["npc.mira"].RecentActions[0].OutcomeEventID != "event.recent-action" {
		t.Fatalf("recent action did not retain the outcome event id: %+v", state.Actors["npc.mira"].RecentActions)
	}

	before, _ := eventStore.counts()
	reuse := invariantObserve(
		sessionID,
		"observe.reuse-recent-action",
		"event.recent-action",
		maxMemories,
	)
	if _, err := engine.Observe(reuse); ErrorCode(err) != "event_exists" {
		t.Fatalf("recent action outcome event id should remain reserved, got %v", err)
	}
	after, _ := eventStore.counts()
	if after != before {
		t.Fatalf("duplicate recent action outcome changed append count from %d to %d", before, after)
	}
}

func TestEventIDExistsIncludesGoalAndObservationReceiptSources(t *testing.T) {
	state := protocol.SessionState{
		Actors: map[string]protocol.ActorState{
			"npc.mira": {
				ActorSeed: protocol.ActorSeed{
					ID: "npc.mira",
					Goals: []protocol.Goal{{
						ID:                  "goal.retained-source",
						StatusSourceEventID: "event.goal-source",
					}},
				},
			},
		},
		Receipts: map[string]protocol.RequestReceipt{
			"observe.retained-source": {
				Kind:     EventObserved,
				EntityID: "event.receipt-source",
			},
			"commit.not-an-event-source": {
				Kind:     EventCommitted,
				EntityID: "proposal.entity",
			},
		},
	}
	if !eventIDExists(state, "event.goal-source") {
		t.Fatal("goal status source event id was not retained")
	}
	if !eventIDExists(state, "event.receipt-source") {
		t.Fatal("observation receipt event id was not retained")
	}
	if eventIDExists(state, "proposal.entity") {
		t.Fatal("non-observation receipt entity id was treated as an event id")
	}
}

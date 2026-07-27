package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
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
	propose func(context.Context, DecisionContext) (DecisionDraft, error)
}

func (p invariantPolicy) Propose(ctx context.Context, input DecisionContext) (DecisionDraft, error) {
	if p.propose != nil {
		return p.propose(ctx, input)
	}
	draft := DecisionDraft{
		OfferID:      input.Request.Offers[0].OfferID,
		Stance:       "wait",
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
	selectedPolicy DecisionProvider,
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
		Epoch:           invariantEpoch(sessionID),
		ObservationSeq:  uint64(tick) + 1,
	}
}

func invariantPropose(sessionID, requestID string, candidateGoals []protocol.Goal) protocol.ProposeRequest {
	window := invariantWindow(sessionID, "npc.mira", 0)
	return protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		ActorID:         "npc.mira",
		Tick:            0,
		Intent:          "Choose a test action.",
		DecisionWindow:  window,
		Offers: []protocol.ActionOffer{
			invariantOffer(window, "npc.mira", "wait"),
		},
		CandidateGoals: append([]protocol.Goal(nil), candidateGoals...),
	}
}

func invariantEpoch(sessionID string) protocol.Epoch {
	return protocol.Epoch{
		SessionID: sessionID,
		WorldID:   "world.test",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
}

func invariantWindow(sessionID, actorID string, tick int64) protocol.DecisionWindow {
	return protocol.DecisionWindow{
		ID:             fmt.Sprintf("window.%s.%d", actorID, tick),
		Mode:           host.DecisionSequential,
		Epoch:          invariantEpoch(sessionID),
		ObservationSeq: uint64(tick) + 1,
		OpenedAt:       protocol.Timepoint{Clock: host.ClockStep, Value: tick},
		Deadline:       protocol.Timepoint{Clock: host.ClockStep, Value: tick + 100},
		ActorIDs:       []string{actorID},
	}
}

func invariantOffer(window protocol.DecisionWindow, actorID, offerID string) protocol.ActionOffer {
	return protocol.ActionOffer{
		OfferID:          offerID,
		DecisionWindowID: window.ID,
		ActorID:          actorID,
		Capability:       protocol.CapabilityRef{ID: "rin.test." + offerID, Version: "1.0.0"},
		DescriptorDigest: strings.Repeat("a", 64),
		Description:      "Wait for more information.",
		Arguments:        json.RawMessage(`{"duration":"short"}`),
		ExpectedEpoch:    window.Epoch,
		ObservationSeq:   window.ObservationSeq,
		Deadline:         window.Deadline,
	}
}

func invariantSuccessfulReport(
	proposal protocol.ActionProposal,
	requestID, eventID string,
	tick int64,
	summary string,
) protocol.ReportActionRequest {
	invocation := protocol.ActionInvocation{
		OperationID:      "operation." + eventID,
		OfferID:          proposal.Action.OfferID,
		DecisionWindowID: proposal.Action.DecisionWindowID,
		ActorID:          proposal.Action.ActorID,
		Capability:       proposal.Action.Capability,
		DescriptorDigest: proposal.Action.DescriptorDigest,
		Arguments:        append(json.RawMessage(nil), proposal.Action.Arguments...),
		Targets:          append([]protocol.HostRef(nil), proposal.Action.Targets...),
		ExpectedEpoch:    proposal.Action.ExpectedEpoch,
		ObservationSeq:   proposal.Action.ObservationSeq,
		Deadline:         proposal.Action.Deadline,
	}
	run := protocol.ActionRun{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		ProgressSeq: 1,
		Progress:    100,
		UpdatedAt:   protocol.Timepoint{Clock: invocation.Deadline.Clock, Value: tick},
	}
	outcome := protocol.ActionOutcome{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     summary,
		Epoch:       invocation.ExpectedEpoch,
		WorldSeq:    1,
		OccurredAt:  protocol.Timepoint{Clock: invocation.Deadline.Clock, Value: tick},
	}
	return protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       proposal.SessionID,
		RequestID:       requestID,
		Tick:            tick,
		Report: protocol.ActionReport{
			ProposalID: proposal.ID,
			EventID:    eventID,
			Decision:   protocol.ActionAccepted,
			Invocation: &invocation,
			Run:        &run,
			Outcome:    &outcome,
			Summary:    summary,
		},
	}
}

func invariantRejectedReport(
	proposal protocol.ActionProposal,
	requestID, eventID string,
	tick int64,
) protocol.ReportActionRequest {
	return protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       proposal.SessionID,
		RequestID:       requestID,
		Tick:            tick,
		Report: protocol.ActionReport{
			ProposalID: proposal.ID,
			EventID:    eventID,
			Decision:   protocol.ActionRejected,
			Summary:    "Rejected.",
		},
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
	session.state.Revision = uint64(protocol.MaxJSONSafeInteger)
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
		t.Fatalf("verifyEvent should reject a successor after the JSON integer ceiling, got %v", err)
	}
}

func TestRevisionOverflowSkipsProposalPolicy(t *testing.T) {
	const sessionID = "session.proposal-revision-overflow"
	policyCalled := false
	selectedPolicy := invariantPolicy{propose: func(context.Context, DecisionContext) (DecisionDraft, error) {
		policyCalled = true
		return DecisionDraft{}, errors.New("policy must not be called")
	}}
	engine, eventStore := invariantEngine(t, sessionID, nil, nil, selectedPolicy)
	session := engine.sessions[sessionID]
	session.mu.Lock()
	session.state.Revision = uint64(protocol.MaxJSONSafeInteger)
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
		session.state.WorldRevision = uint64(protocol.MaxJSONSafeInteger)
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

	t.Run("action_report", func(t *testing.T) {
		const sessionID = "session.world-overflow-action-report"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureArbitration},
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
			_, err := engine.ReportAction(invariantSuccessfulReport(
				proposal,
				"report.world-overflow",
				"event.world-overflow",
				0,
				"The action cannot advance an exhausted world revision.",
			))
			return err
		})
	})

	t.Run("batch", func(t *testing.T) {
		const sessionID = "session.world-overflow-batch"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureArbitration},
			nil,
			invariantPolicy{},
		)
		propose := invariantPropose(sessionID, "propose.world-overflow", nil)
		propose.DecisionWindow.Mode = host.DecisionSimultaneous
		proposal, _, err := engine.Propose(
			context.Background(),
			propose,
		)
		if err != nil {
			t.Fatal(err)
		}
		assertOverflow(t, engine, eventStore, sessionID, func() error {
			report := invariantSuccessfulReport(
				proposal,
				"report.world-overflow",
				"event.world-overflow",
				0,
				"The batch cannot advance an exhausted world revision.",
			)
			_, err := engine.ReportActionBatch(protocol.BatchActionReportRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "batch.world-overflow",
				Reports:         []protocol.ActionReport{report.Report},
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

func TestDecisionContextMutationCannotReachLiveStateOrCaller(t *testing.T) {
	const sessionID = "session.policy-isolation"
	var actorCameFromStateCopy bool
	injected := errors.New("injected policy failure")
	selectedPolicy := invariantPolicy{propose: func(_ context.Context, input DecisionContext) (DecisionDraft, error) {
		input.Actor.Metadata["policy"] = "mutated"
		input.Actor.Goals[0].Description = "mutated through actor context"
		actorCameFromStateCopy = input.State.Actors[input.Actor.ID].Metadata["policy"] == "mutated"
		stateActor := input.State.Actors[input.Actor.ID]
		stateActor.DisplayName = "Mutated State"
		input.State.Actors[input.Actor.ID] = stateActor
		input.Request.Tags[0] = "mutated"
		input.Request.Offers[0].Arguments[0] = '['
		return DecisionDraft{}, injected
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

func TestDecisionContextMutationIsRaceIsolated(t *testing.T) {
	const sessionID = "session.policy-race-isolation"
	started := make(chan struct{})
	release := make(chan struct{})
	selectedPolicy := invariantPolicy{propose: func(_ context.Context, input DecisionContext) (DecisionDraft, error) {
		close(started)
		<-release
		for index := 0; index < 2_000; index++ {
			input.Actor.Goals[0].Description = fmt.Sprintf("policy mutation %d", index)
			input.Request.Offers[0].Description = fmt.Sprintf("request mutation %d", index)
			goruntime.Gosched()
		}
		return DecisionDraft{
			OfferID:      input.Request.Offers[0].OfferID,
			GoalID:       "goal.existing",
			Stance:       "wait",
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
	originalDescription := request.Offers[0].Description
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
		if request.Offers[0].Description != originalDescription {
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

	t.Run("action_report", func(t *testing.T) {
		const sessionID = "session.visibility-action-report"
		engine, eventStore := invariantEngine(t, sessionID, nil, nil, invariantPolicy{})
		proposal, _, err := engine.Propose(context.Background(), invariantPropose(sessionID, "propose.visibility", nil))
		if err != nil {
			t.Fatal(err)
		}
		before, _ := eventStore.counts()
		report := invariantSuccessfulReport(proposal, "report.visibility", "event.visibility", 0, "The actor waited.")
		report.Report.Facts = []protocol.Fact{unknownFact}
		_, err = engine.ReportAction(report)
		if ErrorCode(err) != "unknown_actor" || ErrorField(err) != "report.facts[0].visibility[1]" {
			t.Fatalf("unknown action report visibility actor should fail precisely, got %v", err)
		}
		after, _ := eventStore.counts()
		if after != before {
			t.Fatalf("invalid action report changed append count from %d to %d", before, after)
		}
	})

	t.Run("batch", func(t *testing.T) {
		const sessionID = "session.visibility-batch"
		engine, eventStore := invariantEngine(
			t,
			sessionID,
			[]string{protocol.FeatureArbitration},
			nil,
			invariantPolicy{},
		)
		propose := invariantPropose(sessionID, "propose.visibility", nil)
		propose.DecisionWindow.Mode = host.DecisionSimultaneous
		proposal, _, err := engine.Propose(context.Background(), propose)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := eventStore.counts()
		report := invariantSuccessfulReport(proposal, "report.visibility", "event.visibility", 0, "The actor waited.")
		report.Report.Facts = []protocol.Fact{unknownFact}
		_, err = engine.ReportActionBatch(protocol.BatchActionReportRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "batch.visibility",
			Reports:         []protocol.ActionReport{report.Report},
		})
		if ErrorCode(err) != "unknown_actor" || ErrorField(err) != "reports[0].facts[0].visibility[1]" {
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
		t.Fatalf("33rd recorded-or-reserved goal should fail, got %v", err)
	}
	after, _ := eventStore.counts()
	if after != before {
		t.Fatalf("rejected goal proposals changed append count from %d to %d", before, after)
	}

	if _, err := engine.ReportAction(invariantSuccessfulReport(
		proposal,
		"report.reserved",
		"event.reserved",
		0,
		"The reserved goal was accepted.",
	)); err != nil {
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

func TestActionReportPathsDefendProposedGoalCapacity(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "action_report"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			sessionID := "session.goal-defense-" + name
			goals := make([]protocol.Goal, 31)
			for index := range goals {
				goals[index] = invariantGoal(fmt.Sprintf("goal.%02d", index))
			}
			features := []string{protocol.FeatureGoalCandidates}
			if batch {
				features = append(features, protocol.FeatureArbitration)
			}
			engine, eventStore := invariantEngine(t, sessionID, features, goals, invariantPolicy{})
			propose := invariantPropose(
				sessionID,
				"propose.capacity-defense",
				[]protocol.Goal{invariantGoal("goal.reserved")},
			)
			if batch {
				propose.DecisionWindow.Mode = host.DecisionSimultaneous
			}
			proposal, _, err := engine.Propose(context.Background(), propose)
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
				report := invariantSuccessfulReport(
					proposal,
					"report.capacity-defense",
					"event.capacity-defense",
					0,
					"Would exceed goal capacity.",
				)
				_, err = engine.ReportActionBatch(protocol.BatchActionReportRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "batch.capacity-defense",
					Reports:         []protocol.ActionReport{report.Report},
				})
			} else {
				_, err = engine.ReportAction(invariantSuccessfulReport(
					proposal,
					"report.capacity-defense",
					"event.capacity-defense",
					0,
					"Would exceed goal capacity.",
				))
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
			var features []string
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
		[]string{protocol.FeatureArbitration},
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
		if _, err := engine.ReportAction(invariantRejectedReport(
			proposal,
			fmt.Sprintf("report.rejected.%02d", index),
			fmt.Sprintf("event.rejected.%02d", index),
			0,
		)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.ReportAction(invariantSuccessfulReport(
		oldest,
		"report.oldest",
		"event.recent-action",
		0,
		"This memory will be evicted while the recent action remains.",
	)); err != nil {
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
		state.Actors["npc.mira"].RecentActions[0].LastReportEventID != "event.recent-action" {
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
			"report.not-an-event-source": {
				Kind:     EventActionReported,
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

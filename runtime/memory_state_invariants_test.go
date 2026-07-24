package runtime

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

func TestMemoryEvictionRewritesActorReferences(t *testing.T) {
	t.Run("archive compaction", func(t *testing.T) {
		state := invariantSessionState(t, protocol.FeatureOutcomeReporting, protocol.FeatureMemoryArchive)
		actor := state.Actors["npc.mira"]
		for index := 0; index < maxMemories+1; index++ {
			actor.Memories = append(actor.Memories, invariantMemory(index))
		}
		state.Tick = maxMemories
		recalled := []string{actor.Memories[0].ID, actor.Memories[1].ID}
		pending := invariantProposal(state, "proposal.pending.archive", "pending", recalled)
		recent := invariantProposal(state, "proposal.recent.archive", "accepted", recalled)
		state.Proposals[pending.ID] = pending
		actor.RecentActions = []protocol.ActionProposal{recent}

		if err := compactActorMemories(state.SessionID, &actor, state.Revision, &state); err != nil {
			t.Fatal(err)
		}
		state.Actors[actor.ID] = actor
		if err := protocol.ValidateSessionState(state); err != nil {
			t.Fatalf("compacted state is invalid: %v", err)
		}
		if len(actor.MemorySummaries) != 1 {
			t.Fatalf("summary count = %d, want 1", len(actor.MemorySummaries))
		}
		summaryID := actor.MemorySummaries[0].ID
		if got := state.Proposals[pending.ID].RecalledMemoryIDs; !reflect.DeepEqual(got, []string{summaryID}) {
			t.Fatalf("pending proposal references = %v, want [%s]", got, summaryID)
		}
		if got := actor.RecentActions[0].RecalledMemoryIDs; !reflect.DeepEqual(got, []string{summaryID}) {
			t.Fatalf("recent action references = %v, want [%s]", got, summaryID)
		}
	})

	t.Run("archive summary merge", func(t *testing.T) {
		state := invariantSessionState(t, protocol.FeatureOutcomeReporting, protocol.FeatureMemoryArchive)
		actor := state.Actors["npc.mira"]
		for index := 0; index < maxMemorySummaries+1; index++ {
			actor.MemorySummaries = append(actor.MemorySummaries, invariantSummary(index, 1, 1))
		}
		state.Tick = maxMemorySummaries
		recalled := []string{actor.MemorySummaries[0].ID, actor.MemorySummaries[1].ID}
		pending := invariantProposal(state, "proposal.pending.merge", "pending", recalled)
		recent := invariantProposal(state, "proposal.recent.merge", "accepted", recalled)
		state.Proposals[pending.ID] = pending
		actor.RecentActions = []protocol.ActionProposal{recent}

		if err := compactActorMemories(state.SessionID, &actor, state.Revision, &state); err != nil {
			t.Fatal(err)
		}
		state.Actors[actor.ID] = actor
		if err := protocol.ValidateSessionState(state); err != nil {
			t.Fatalf("merged state is invalid: %v", err)
		}
		if len(actor.MemorySummaries) != maxMemorySummaries-summaryMergeBatch+2 {
			t.Fatalf("summary count = %d", len(actor.MemorySummaries))
		}
		mergedID := actor.MemorySummaries[0].ID
		if got := state.Proposals[pending.ID].RecalledMemoryIDs; !reflect.DeepEqual(got, []string{mergedID}) {
			t.Fatalf("pending proposal references = %v, want [%s]", got, mergedID)
		}
		if got := actor.RecentActions[0].RecalledMemoryIDs; !reflect.DeepEqual(got, []string{mergedID}) {
			t.Fatalf("recent action references = %v, want [%s]", got, mergedID)
		}
	})

	t.Run("non archive eviction", func(t *testing.T) {
		state := invariantSessionState(t, protocol.FeatureOutcomeReporting)
		actor := state.Actors["npc.mira"]
		for index := 0; index < maxMemories+1; index++ {
			actor.Memories = append(actor.Memories, invariantMemory(index))
		}
		state.Tick = maxMemories
		recalled := []string{actor.Memories[0].ID, actor.Memories[1].ID}
		pending := invariantProposal(state, "proposal.pending.evict", "pending", recalled)
		recent := invariantProposal(state, "proposal.recent.evict", "accepted", recalled)
		state.Proposals[pending.ID] = pending
		actor.RecentActions = []protocol.ActionProposal{recent}
		retainedID := actor.Memories[1].ID

		trimActorMemories(&state, &actor)
		state.Actors[actor.ID] = actor
		if err := protocol.ValidateSessionState(state); err != nil {
			t.Fatalf("trimmed state is invalid: %v", err)
		}
		if got := state.Proposals[pending.ID].RecalledMemoryIDs; !reflect.DeepEqual(got, []string{retainedID}) {
			t.Fatalf("pending proposal references = %v, want [%s]", got, retainedID)
		}
		if got := actor.RecentActions[0].RecalledMemoryIDs; !reflect.DeepEqual(got, []string{retainedID}) {
			t.Fatalf("recent action references = %v, want [%s]", got, retainedID)
		}
	})
}

func TestReducerMaintainsBoundsAcross1361Observations(t *testing.T) {
	state := invariantSessionState(
		t,
		protocol.FeatureOutcomeReporting,
		protocol.FeatureMemoryArchive,
		protocol.FeatureBeliefConflicts,
		protocol.FeatureArbitration,
	)
	for index := 0; index < 1361; index++ {
		request := protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       state.SessionID,
			RequestID:       fmt.Sprintf("observe.%04d", index),
			EventID:         fmt.Sprintf("event.%04d", index),
			Tick:            int64(index + 1),
			ObserverIDs:     []string{"npc.mira"},
			Source:          "game",
			Kind:            "world",
			Summary:         "A deterministic long-run observation.",
			Importance:      2,
			Facts: []protocol.Fact{{
				SubjectID:  fmt.Sprintf("subject.%04d", index),
				Predicate:  "state",
				Object:     "known",
				Visibility: []string{"npc.mira"},
				Confidence: 80,
			}},
		}
		event := invariantEvent(t, state, EventObserved, request.RequestID, observedPayload{Request: request}, index+2)
		var err error
		state, err = applyEvent(state, event)
		if err != nil {
			t.Fatalf("observation %d failed: %v", index, err)
		}
	}

	actor := state.Actors["npc.mira"]
	if len(actor.Memories) > maxMemories || len(actor.MemorySummaries) > maxMemorySummaries {
		t.Fatalf("memory bounds exceeded: memories=%d summaries=%d", len(actor.Memories), len(actor.MemorySummaries))
	}
	for _, summary := range actor.MemorySummaries {
		if summary.Level > maxMemorySummaryLevel {
			t.Fatalf("summary %s level = %d", summary.ID, summary.Level)
		}
	}
	if len(actor.Beliefs) != maxBeliefs || len(actor.BeliefSets) != maxBeliefs {
		t.Fatalf("belief bounds = %d/%d, want %d/%d", len(actor.Beliefs), len(actor.BeliefSets), maxBeliefs, maxBeliefs)
	}
	if _, exists := actor.Beliefs["subject.0000:state"]; exists {
		t.Fatal("oldest occurrence survived deterministic capacity eviction")
	}
	if _, exists := actor.Beliefs["subject.1360:state"]; !exists {
		t.Fatal("newest occurrence was evicted")
	}
	if len(state.Receipts) != maxReceipts {
		t.Fatalf("receipt count = %d, want %d", len(state.Receipts), maxReceipts)
	}
	if state.WorldRevision != 1362 {
		t.Fatalf("world revision = %d, want 1362", state.WorldRevision)
	}
	if err := protocol.ValidateSessionState(state); err != nil {
		t.Fatalf("long-run state is invalid: %v", err)
	}
}

func TestRecallAndSummaryBoundsSaturate(t *testing.T) {
	actor := protocol.ActorState{
		Memories: []protocol.Memory{{
			ID: "memory.max", RecallCount: maxRecallCount,
		}},
		MemorySummaries: []protocol.MemorySummary{{
			ID: "summary.max", RecallCount: maxRecallCount,
		}},
	}
	markRecalled(&actor, []string{"memory.max", "summary.max"}, 9, true)
	if actor.Memories[0].RecallCount != maxRecallCount || actor.MemorySummaries[0].RecallCount != maxRecallCount {
		t.Fatalf("recall counts exceeded the bound: memory=%d summary=%d",
			actor.Memories[0].RecallCount,
			actor.MemorySummaries[0].RecallCount,
		)
	}
	if actor.Memories[0].LastRecalledTick != 9 || actor.MemorySummaries[0].LastRecalledTick != 9 {
		t.Fatal("saturated recall count must still record the latest occurrence")
	}

	summaries := make([]protocol.MemorySummary, summaryMergeBatch)
	for index := range summaries {
		summaries[index] = invariantSummary(index, maxMemorySummaryLevel, 300_000)
	}
	merged, err := mergeMemorySummaries("session.bounds", "npc.mira", summaries, 1)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Level != maxMemorySummaryLevel {
		t.Fatalf("merged level = %d, want %d", merged.Level, maxMemorySummaryLevel)
	}
	if merged.RecallCount != maxRecallCount {
		t.Fatalf("merged recall count = %d, want %d", merged.RecallCount, maxRecallCount)
	}
}

func TestMergeMemorySummariesPreservesFullTickRange(t *testing.T) {
	summaries := make([]protocol.MemorySummary, summaryMergeBatch)
	ranges := [][2]int64{{1, 100}, {2, 20}, {3, 30}, {4, 40}}
	for index := range summaries {
		summaries[index] = invariantSummary(index, 1, 0)
		summaries[index].StartTick = ranges[index][0]
		summaries[index].EndTick = ranges[index][1]
	}
	merged, err := mergeMemorySummaries("session.range", "npc.mira", summaries, 1)
	if err != nil {
		t.Fatal(err)
	}
	if merged.StartTick != 1 || merged.EndTick != 100 {
		t.Fatalf("merged tick range = %d..%d, want 1..100", merged.StartTick, merged.EndTick)
	}
}

func TestRestoreRebasesAllFeaturesAndKeepsReceiptWhenFull(t *testing.T) {
	source := invariantSessionState(t)
	actor := source.Actors["npc.mira"]
	actor.Memories = []protocol.Memory{invariantMemory(0)}
	actor.RecentActions = []protocol.ActionProposal{
		invariantProposal(source, "proposal.recent.restore", "accepted", []string{actor.Memories[0].ID}),
	}
	source.Actors[actor.ID] = actor
	source.Receipts = make(map[string]protocol.RequestReceipt, maxReceipts)
	for index := 0; index < maxReceipts; index++ {
		revision := uint64(0)
		if index == maxReceipts-1 {
			revision = source.Revision
		}
		source.Receipts[fmt.Sprintf("receipt.%04d", index)] = protocol.RequestReceipt{
			Kind:     EventObserved,
			EntityID: fmt.Sprintf("entity.%04d", index),
			Revision: revision,
		}
	}
	if err := protocol.ValidateSessionState(source); err != nil {
		t.Fatalf("source state is invalid: %v", err)
	}
	snapshot, err := SnapshotOf(source)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		current  protocol.SessionState
		request  string
		revision uint64
	}{
		{name: "fresh", request: "restore.fresh.full", revision: 1},
		{name: "existing", current: source, request: "restore.existing.full", revision: 2},
	}
	for caseIndex, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event := invariantEvent(
				t,
				testCase.current,
				EventSessionRestored,
				testCase.request,
				restoredPayload{Snapshot: snapshot},
				2000+caseIndex,
			)
			restored, err := applyEvent(testCase.current, event)
			if err != nil {
				t.Fatal(err)
			}
			if len(restored.Receipts) != maxReceipts {
				t.Fatalf("receipt count = %d, want %d", len(restored.Receipts), maxReceipts)
			}
			if receipt, exists := restored.Receipts[testCase.request]; !exists || receipt.Revision != testCase.revision {
				t.Fatalf("restore receipt was not retained: %+v exists=%v", receipt, exists)
			}
			for requestID, receipt := range restored.Receipts {
				if requestID != testCase.request && receipt.Revision != 0 {
					t.Fatalf("old receipt %s retained revision %d", requestID, receipt.Revision)
				}
			}
			restoredActor := restored.Actors["npc.mira"]
			if restoredActor.Memories[0].CreatedRevision != testCase.revision {
				t.Fatalf("memory revision = %d, want %d", restoredActor.Memories[0].CreatedRevision, testCase.revision)
			}
			recent := restoredActor.RecentActions[0]
			if recent.CreatedRevision != testCase.revision ||
				recent.BasedOnRevision != testCase.revision-1 ||
				recent.BasedOnHeadHash != event.PrevHash {
				t.Fatalf("recent action generation was not rebased: %+v", recent)
			}
		})
	}
}

func TestRestoreRebasesPendingProposalGenerationRoundTrip(t *testing.T) {
	source := invariantSessionState(t, protocol.FeatureOutcomeReporting)
	proposal := invariantProposal(source, "proposal.pending.restore", "pending", nil)
	source.Proposals[proposal.ID] = proposal
	if err := protocol.ValidateSessionState(source); err != nil {
		t.Fatalf("source state is invalid: %v", err)
	}
	snapshot, err := SnapshotOf(source)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		current  protocol.SessionState
		request  string
		revision uint64
	}{
		{name: "fresh", request: "restore.pending.fresh", revision: 1},
		{name: "existing", current: source, request: "restore.pending.existing", revision: 2},
	}
	for caseIndex, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event := invariantEvent(
				t,
				testCase.current,
				EventSessionRestored,
				testCase.request,
				restoredPayload{Snapshot: snapshot},
				2500+caseIndex,
			)
			restored, err := applyEvent(testCase.current, event)
			if err != nil {
				t.Fatal(err)
			}
			pending, exists := restored.Proposals[proposal.ID]
			if !exists {
				t.Fatal("pending proposal was not retained")
			}
			if pending.CreatedRevision != testCase.revision ||
				pending.BasedOnRevision != testCase.revision-1 ||
				pending.BasedOnHeadHash != event.PrevHash {
				t.Fatalf("pending proposal generation was not rebased: %+v", pending)
			}
			roundTrip, err := SnapshotOf(restored)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSnapshot(roundTrip); err != nil {
				t.Fatalf("rebased proposal did not round-trip through a snapshot: %v", err)
			}
		})
	}
}

func TestWorldRevisionOverflowAndRestoreSaturation(t *testing.T) {
	state := invariantSessionState(t, protocol.FeatureOutcomeReporting, protocol.FeatureArbitration)
	state.WorldRevision = ^uint64(0)
	pending := invariantProposal(state, "proposal.pending.world-max", "pending", nil)
	state.Proposals[pending.ID] = pending
	snapshot, err := SnapshotOf(state)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       state.SessionID,
		RequestID:       "observe.world-overflow",
		EventID:         "event.world-overflow",
		Tick:            1,
		ObserverIDs:     []string{"npc.mira"},
		Source:          "game",
		Kind:            "world",
		Summary:         "World revision cannot wrap.",
		Importance:      1,
	}
	event := invariantEvent(t, state, EventObserved, request.RequestID, observedPayload{Request: request}, 3000)
	if _, err := applyEvent(state, event); !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("observation overflow error = %v, want ErrCorruptLog", err)
	}

	restore := invariantEvent(
		t,
		protocol.SessionState{},
		EventSessionRestored,
		"restore.world-overflow",
		restoredPayload{Snapshot: snapshot},
		3001,
	)
	restored, err := applyEvent(protocol.SessionState{}, restore)
	if err != nil {
		t.Fatalf("max world snapshot must remain restorable: %v", err)
	}
	if restored.WorldRevision != ^uint64(0) {
		t.Fatalf("restored world revision = %d, want saturation at max", restored.WorldRevision)
	}
	restoredPending, exists := restored.Proposals[pending.ID]
	if !exists || restoredPending.BasedOnWorldRevision != ^uint64(0) {
		t.Fatalf("pending proposal was not retained at the saturated world generation: %+v", restoredPending)
	}
	roundTrip, err := SnapshotOf(restored)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshot(roundTrip); err != nil {
		t.Fatalf("max world restore did not round-trip: %v", err)
	}
}

func TestGoalCandidateAtCapacityFailsClosed(t *testing.T) {
	state := invariantSessionState(t, protocol.FeatureGoalCandidates)
	actor := state.Actors["npc.mira"]
	for index := 0; index < maxGoals; index++ {
		actor.Goals = append(actor.Goals, protocol.Goal{
			ID:             fmt.Sprintf("goal.%02d", index),
			Description:    "A bounded existing goal.",
			Priority:       1,
			TargetProgress: 1,
			Status:         "active",
		})
	}
	state.Actors[actor.ID] = actor
	proposal := invariantProposal(state, "proposal.goal-overflow", "pending", nil)
	proposal.GoalID = "goal.new"
	proposal.ProposedGoal = &protocol.Goal{
		ID:             proposal.GoalID,
		Description:    "This goal cannot be appended.",
		Priority:       1,
		TargetProgress: 1,
		Status:         "active",
	}
	state.Proposals[proposal.ID] = proposal
	if err := protocol.ValidateSessionState(state); err == nil {
		t.Fatal("test setup must violate the pending goal reservation invariant")
	}
	beforeHash, err := hashJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       state.SessionID,
		RequestID:       "commit.goal-overflow",
		ProposalID:      proposal.ID,
		EventID:         "event.goal-overflow",
		Tick:            1,
		Accepted:        true,
		Outcome:         "The action occurred.",
	}
	event := invariantEvent(t, state, EventCommitted, request.RequestID, committedPayload{Request: request}, 4000)
	store := &invariantSpyStore{}
	engine := &Engine{store: store}
	session := &managedSession{state: state}
	if err := engine.appendAndApply(session, event); !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("goal capacity error = %v, want ErrCorruptLog", err)
	}
	afterHash, err := hashJSON(session.state)
	if err != nil {
		t.Fatal(err)
	}
	if store.appendCalls != 0 || afterHash != beforeHash {
		t.Fatalf("failed goal append escaped isolation: appends=%d state_changed=%v", store.appendCalls, afterHash != beforeHash)
	}
}

func TestInvalidReducedStateIsNotAppendedOrPublished(t *testing.T) {
	state := invariantSessionState(t)
	request := protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       state.SessionID,
		RequestID:       "observe.invalid-visibility",
		EventID:         "event.invalid-visibility",
		Tick:            1,
		ObserverIDs:     []string{"npc.mira"},
		Source:          "game",
		Kind:            "world",
		Summary:         "The fact includes an unknown visibility target.",
		Importance:      1,
		Facts: []protocol.Fact{{
			SubjectID:  "door",
			Predicate:  "state",
			Object:     "open",
			Visibility: []string{"npc.mira", "npc.unknown"},
			Confidence: 100,
		}},
	}
	event := invariantEvent(t, state, EventObserved, request.RequestID, observedPayload{Request: request}, 5000)
	beforeHash, err := hashJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	store := &invariantSpyStore{}
	engine := &Engine{store: store}
	session := &managedSession{state: state}
	err = engine.appendAndApply(session, event)
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("appendAndApply error = %v, want ErrCorruptLog", err)
	}
	if store.appendCalls != 0 {
		t.Fatalf("invalid state reached Store.Append %d times", store.appendCalls)
	}
	afterHash, err := hashJSON(session.state)
	if err != nil {
		t.Fatal(err)
	}
	if afterHash != beforeHash {
		t.Fatal("invalid state was published to the managed session")
	}
}

func invariantSessionState(t *testing.T, features ...string) protocol.SessionState {
	t.Helper()
	request := protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.invariant",
		SessionID:       "session.invariant",
		Binding: protocol.Binding{
			GameID:         "game",
			ContentID:      "content",
			ContentVersion: "1",
			ContentHash:    "content-hash",
		},
		Features: features,
		Actors: []protocol.ActorSeed{{
			ID:              "npc.mira",
			Kind:            "npc",
			DisplayName:     "Mira",
			ThinkEveryTicks: 5,
			Enabled:         true,
		}},
	}
	event := invariantEvent(
		t,
		protocol.SessionState{},
		EventSessionCreated,
		request.RequestID,
		createdPayload{Request: request},
		1,
	)
	state, err := applyEvent(protocol.SessionState{}, event)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func invariantEvent(
	t *testing.T,
	state protocol.SessionState,
	eventType string,
	requestID string,
	payload any,
	second int,
) protocol.EventRecord {
	t.Helper()
	event, err := newEvent(state, eventType, requestID, payload, time.Unix(int64(second), 0))
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func invariantMemory(index int) protocol.Memory {
	return protocol.Memory{
		ID:              "memory." + fixedID(index),
		EventID:         "event.memory." + fixedID(index),
		Tick:            int64(index),
		Summary:         "A bounded memory.",
		Importance:      2,
		CreatedRevision: 1,
	}
}

func invariantSummary(index, level, recallCount int) protocol.MemorySummary {
	return protocol.MemorySummary{
		ID:              "summary." + fixedID(index),
		Level:           level,
		Summary:         "A bounded summary.",
		SourceMemoryIDs: []string{"source.memory." + fixedID(index)},
		SourceEventIDs:  []string{"source.event." + fixedID(index)},
		StartTick:       int64(index),
		EndTick:         int64(index),
		Importance:      1,
		Reason:          "episodic_capacity",
		CreatedRevision: 1,
		RecallCount:     recallCount,
	}
}

func invariantProposal(
	state protocol.SessionState,
	id string,
	status string,
	recalled []string,
) protocol.ActionProposal {
	proposal := protocol.ActionProposal{
		ID:                   id,
		SessionID:            state.SessionID,
		RequestID:            "request." + id,
		ActorID:              "npc.mira",
		BasedOnRevision:      state.Revision - 1,
		CreatedRevision:      state.Revision,
		BasedOnWorldRevision: state.WorldRevision,
		Action: protocol.ActionSpec{
			ID:          "action.wait",
			Kind:        "wait",
			Description: "Wait carefully.",
		},
		Stance:            "wait",
		Summary:           "Wait carefully.",
		Rationale:         "A deterministic test action.",
		PolicySource:      "test",
		RecalledMemoryIDs: append([]string(nil), recalled...),
		Status:            status,
	}
	if proposal.BasedOnRevision > 0 {
		proposal.BasedOnHeadHash = state.HeadHash
	}
	if !protocol.HasFeature(state.Features, protocol.FeatureArbitration) {
		proposal.BasedOnWorldRevision = 0
	}
	if status != "pending" && protocol.HasFeature(state.Features, protocol.FeatureOutcomeReporting) {
		proposal.OutcomeEventID = "outcome." + id
		proposal.OutcomeTick = state.Tick
	}
	return proposal
}

type invariantSpyStore struct {
	appendCalls int
}

func (store *invariantSpyStore) Create(string, protocol.EventRecord) error {
	return nil
}

func (store *invariantSpyStore) Append(string, protocol.EventRecord) error {
	store.appendCalls++
	return nil
}

func (store *invariantSpyStore) Load(string) ([]protocol.EventRecord, error) {
	return nil, ErrNotFound
}

func (store *invariantSpyStore) ListSessions() ([]string, error) {
	return nil, nil
}

func (store *invariantSpyStore) SaveSnapshot(string, protocol.Snapshot) error {
	return nil
}

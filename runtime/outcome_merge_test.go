package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestSafeBaselineSupportsEveryOptionalFeatureCombination(t *testing.T) {
	optional := []string{
		protocol.FeatureMemoryArchive,
		protocol.FeatureBeliefConflicts,
		protocol.FeatureGoalCandidates,
		protocol.FeatureActorActivity,
		protocol.FeatureArbitration,
	}
	for mask := 0; mask < 1<<len(optional); mask++ {
		sessionID := fmt.Sprintf("session.safe-profile.%02d", mask)
		create := createRequest(sessionID)
		for index, feature := range optional {
			if mask&(1<<index) != 0 {
				create.Features = append(create.Features, feature)
			}
		}
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatalf("mask %02d Create: %v", mask, err)
		}
		state, err := engine.State(sessionRequest(sessionID))
		if err != nil {
			t.Fatalf("mask %02d State: %v", mask, err)
		}
		if err := protocol.ValidateSessionState(state); err != nil {
			t.Fatalf("mask %02d state: %v", mask, err)
		}
	}
}

func TestLateActionReportMergesDerivedStateByOccurrenceTick(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	const sessionID = "session.late-merge"
	create := createRequest(sessionID)
	create.Features = append(create.Features, protocol.FeatureBeliefConflicts)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(observeRequest(sessionID, "observe.seed", "event.seed", 0)); err != nil {
		t.Fatal(err)
	}

	older, _, err := engine.Propose(context.Background(), proposeRequest(sessionID, "propose.older", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	newer, _, err := engine.Propose(context.Background(), proposeRequest(sessionID, "propose.newer", 10, nil))
	if err != nil {
		t.Fatal(err)
	}

	newerReport := successfulReportRequest(
		newer, "report.newer", "event.newer", 10, "The newer action happened.",
	)
	newerReport.Report.Facts = []protocol.Fact{{
		SubjectID: "door", Predicate: "state", Object: "open", Confidence: 80,
	}}
	newerReport.Report.GoalUpdates = []protocol.GoalUpdate{{
		GoalID: "goal.connect", Status: "released",
	}}
	if _, err := engine.ReportAction(newerReport); err != nil {
		t.Fatal(err)
	}
	olderReport := successfulReportRequest(
		older, "report.older", "event.older", 0, "The older action report arrived late.",
	)
	olderReport.Report.Facts = []protocol.Fact{{
		SubjectID: "door", Predicate: "state", Object: "closed", Confidence: 100,
	}}
	olderReport.Report.GoalUpdates = []protocol.GoalUpdate{{
		GoalID: "goal.connect", Status: "active",
	}}
	if _, err := engine.ReportAction(olderReport); err != nil {
		t.Fatalf("late outcome should merge without regressing newer state: %v", err)
	}

	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actors["npc.mira"]
	if state.Tick != 10 || actor.NextThinkTick != 15 {
		t.Fatalf("late outcome regressed scheduler state: tick=%d next=%d", state.Tick, actor.NextThinkTick)
	}
	if len(actor.RecentActions) != 2 ||
		actor.RecentActions[0].ID != older.ID ||
		actor.RecentActions[1].ID != newer.ID {
		t.Fatalf("recent actions are not occurrence ordered: %+v", actor.RecentActions)
	}
	if got := actor.Beliefs["door:state"]; got.Object != "open" || got.ObservedTick != 10 {
		t.Fatalf("late fact replaced a newer game fact: %+v", got)
	}
	if set := actor.BeliefSets["door:state"]; len(set.Claims) != 2 ||
		set.SelectedSourceEventID != "event.newer" {
		t.Fatalf("conflict projection did not prefer the newer occurrence: %+v", set)
	}
	goal, found := findGoal(actor, "goal.connect")
	if !found ||
		goal.Status != "released" ||
		goal.UpdatedTick != 10 ||
		goal.StatusUpdatedTick != 10 ||
		goal.StatusSourceEventID != "event.newer" ||
		goal.Progress != 2 {
		t.Fatalf("late goal update regressed status or lost commutative progress: %+v", goal)
	}
	if len(older.RecalledMemoryIDs) == 0 {
		t.Fatal("test setup expected both proposals to recall the seed memory")
	}
	for _, memory := range actor.Memories {
		if memory.ID == older.RecalledMemoryIDs[0] &&
			(memory.RecallCount != 2 || memory.LastRecalledTick != 10) {
			t.Fatalf("late recall regressed recall metadata: %+v", memory)
		}
	}
	if len(actor.Memories) != 3 ||
		actor.Memories[0].Tick > actor.Memories[1].Tick ||
		actor.Memories[1].Tick > actor.Memories[2].Tick {
		t.Fatalf("memories are not occurrence ordered: %+v", actor.Memories)
	}
	snapshot, err := engine.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatalf("late-merged state must remain snapshot-compatible: %v", err)
	}
	if err := rinruntime.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("late-merged snapshot is not restorable: %v", err)
	}
}

func TestBeliefConflictCapacityKeepsNewestOccurrences(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	const sessionID = "session.belief-occurrence-capacity"
	create := createRequest(sessionID)
	create.Features = append(create.Features, protocol.FeatureBeliefConflicts)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	for tick := int64(10); tick >= 1; tick-- {
		request := observeRequest(
			sessionID,
			"observe.belief-"+string(rune('a'+tick)),
			"event.belief-"+string(rune('a'+tick)),
			tick,
		)
		request.Facts = []protocol.Fact{{
			SubjectID: "gate",
			Predicate: "state",
			Object:    "state-" + string(rune('a'+tick)),
			Confidence: func() int {
				if tick <= 2 {
					return 100
				}
				return 50
			}(),
		}}
		if _, err := engine.Observe(request); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	set := state.Actors["npc.mira"].BeliefSets["gate:state"]
	if len(set.Claims) != 8 {
		t.Fatalf("belief claim count = %d, want 8: %+v", len(set.Claims), set)
	}
	minimumTick := int64(10)
	for _, claim := range set.Claims {
		if claim.Fact.ObservedTick < minimumTick {
			minimumTick = claim.Fact.ObservedTick
		}
		if claim.Fact.ObservedTick <= 2 {
			t.Fatalf("old high-confidence claim survived occurrence-first trimming: %+v", claim)
		}
	}
	selected := state.Actors["npc.mira"].Beliefs["gate:state"]
	if minimumTick != 3 || selected.ObservedTick != 10 {
		t.Fatalf("belief capacity did not retain/select newest occurrences: set=%+v selected=%+v", set, selected)
	}
}

func TestLateBatchReportMergesByOccurrenceTick(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	create := twoActorWorldRequest("session.late-batch-merge")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}

	oldMira, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.old-mira", "npc.mira"))
	if err != nil {
		t.Fatal(err)
	}
	oldOren, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.old-oren", "npc.oren"))
	if err != nil {
		t.Fatal(err)
	}
	advance := observeRequest(create.SessionID, "observe.advance-batch", "event.advance-batch", 10)
	advance.ObserverIDs = []string{"npc.mira", "npc.oren"}
	if _, err := engine.Observe(advance); err != nil {
		t.Fatal(err)
	}
	newMiraRequest := targetedProposalRequest(create.SessionID, "propose.new-mira", "npc.mira")
	newMiraRequest.Tick = 10
	newMira, _, err := engine.Propose(context.Background(), newMiraRequest)
	if err != nil {
		t.Fatal(err)
	}
	newOrenRequest := targetedProposalRequest(create.SessionID, "propose.new-oren", "npc.oren")
	newOrenRequest.Tick = 10
	newOren, _, err := engine.Propose(context.Background(), newOrenRequest)
	if err != nil {
		t.Fatal(err)
	}

	newFacts := []protocol.Fact{{SubjectID: "camera", Predicate: "state", Object: "repaired", Confidence: 80}}
	newMiraReport := successfulReportRequest(
		newMira, "unused", "event.batch-new-mira", 10, "Mira repaired it.",
	).Report
	newMiraReport.Facts = newFacts
	newOrenReport := successfulReportRequest(
		newOren, "unused", "event.batch-new-oren", 10, "Oren documented it.",
	).Report
	newOrenReport.Facts = newFacts
	if _, err := engine.ReportActionBatch(protocol.BatchActionReportRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "report.batch-new",
		Tick:            10,
		Reports:         []protocol.ActionReport{newMiraReport, newOrenReport},
	}); err != nil {
		t.Fatal(err)
	}
	oldFacts := []protocol.Fact{{SubjectID: "camera", Predicate: "state", Object: "damaged", Confidence: 100}}
	oldMiraReport := successfulReportRequest(
		oldMira, "unused", "event.batch-old-mira", 0, "Mira first inspected it.",
	).Report
	oldMiraReport.Facts = oldFacts
	oldOrenReport := successfulReportRequest(
		oldOren, "unused", "event.batch-old-oren", 0, "Oren first inspected it.",
	).Report
	oldOrenReport.Facts = oldFacts
	if _, err := engine.ReportActionBatch(protocol.BatchActionReportRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "report.batch-old",
		Tick:            0,
		Reports:         []protocol.ActionReport{oldMiraReport, oldOrenReport},
	}); err != nil {
		t.Fatalf("late batch outcome should be recorded: %v", err)
	}

	state, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Tick != 10 {
		t.Fatalf("late batch regressed session tick to %d", state.Tick)
	}
	for actorID, ids := range map[string][2]string{
		"npc.mira": {oldMira.ID, newMira.ID},
		"npc.oren": {oldOren.ID, newOren.ID},
	} {
		actor := state.Actors[actorID]
		if actor.NextThinkTick != 15 {
			t.Fatalf("%s scheduler regressed to %d", actorID, actor.NextThinkTick)
		}
		if len(actor.RecentActions) != 2 ||
			actor.RecentActions[0].ID != ids[0] ||
			actor.RecentActions[1].ID != ids[1] {
			t.Fatalf("%s actions are not occurrence ordered: %+v", actorID, actor.RecentActions)
		}
		if got := actor.Beliefs["camera:state"]; got.Object != "repaired" || got.ObservedTick != 10 {
			t.Fatalf("%s late fact replaced newer state: %+v", actorID, got)
		}
	}
	snapshot, err := engine.Snapshot(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatalf("late batch state must remain snapshot-compatible: %v", err)
	}
	if err := rinruntime.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("late batch snapshot is not restorable: %v", err)
	}
}

func TestGoalProgressDeltasAreIndependentOfOutcomeArrivalOrder(t *testing.T) {
	type outcomeSpec struct {
		name  string
		tick  int64
		delta int
	}
	older := outcomeSpec{name: "older", tick: 0, delta: -3}
	newer := outcomeSpec{name: "newer", tick: 10, delta: 2}
	orders := [][]outcomeSpec{{newer, older}, {older, newer}}

	for index, order := range orders {
		sessionID := "session.goal-delta-order-" + string(rune('a'+index))
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		create := createRequest(sessionID)
		create.Actors[0].Goals[0].Progress = 1
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}

		proposals := make(map[string]protocol.ActionProposal, len(order))
		for _, spec := range []outcomeSpec{older, newer} {
			proposal, _, err := engine.Propose(
				context.Background(),
				proposeRequest(sessionID, "propose."+spec.name, spec.tick, nil),
			)
			if err != nil {
				t.Fatal(err)
			}
			proposals[spec.name] = proposal
		}
		for _, spec := range order {
			report := successfulReportRequest(
				proposals[spec.name],
				"report."+spec.name,
				"event."+spec.name,
				spec.tick,
				"The game applied the "+spec.name+" action.",
			)
			report.Report.GoalUpdates = []protocol.GoalUpdate{{
				GoalID: "goal.connect", ProgressDelta: spec.delta,
			}}
			if _, err := engine.ReportAction(report); err != nil {
				t.Fatal(err)
			}
		}

		state, err := engine.State(sessionRequest(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		goal, found := findGoal(state.Actors["npc.mira"], "goal.connect")
		if !found ||
			goal.Progress != 2 ||
			goal.ProgressAccumulator != 2 ||
			goal.Status != "active" ||
			goal.StatusExplicit {
			t.Fatalf("arrival order %d produced order-dependent progress: %+v", index, goal)
		}
	}
}

func TestGoalStatusOrderingIsIndependentFromProgressOnlyUpdates(t *testing.T) {
	type outcomeSpec struct {
		name   string
		tick   int64
		status string
		delta  int
	}
	early := outcomeSpec{name: "early-status", tick: 10, status: "released"}
	middle := outcomeSpec{name: "middle-status", tick: 15, status: "completed"}
	late := outcomeSpec{name: "late-progress", tick: 20, delta: 2}
	orders := [][]outcomeSpec{
		{early, middle, late},
		{early, late, middle},
		{middle, early, late},
		{middle, late, early},
		{late, early, middle},
		{late, middle, early},
	}

	for index, order := range orders {
		sessionID := "session.goal-status-order-" + string(rune('a'+index))
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		create := createRequest(sessionID)
		create.Actors[0].Goals[0].TargetProgress = 100
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}

		proposals := make(map[string]protocol.ActionProposal, len(order))
		for _, spec := range []outcomeSpec{early, middle, late} {
			proposal, _, err := engine.Propose(
				context.Background(),
				proposeRequest(sessionID, "propose."+spec.name, spec.tick, nil),
			)
			if err != nil {
				t.Fatal(err)
			}
			proposals[spec.name] = proposal
		}
		for _, spec := range order {
			report := successfulReportRequest(
				proposals[spec.name],
				"report."+spec.name,
				"event."+spec.name,
				spec.tick,
				"The game applied "+spec.name+".",
			)
			report.Report.GoalUpdates = []protocol.GoalUpdate{{
				GoalID:        "goal.connect",
				ProgressDelta: spec.delta,
				Status:        spec.status,
			}}
			if _, err := engine.ReportAction(report); err != nil {
				t.Fatal(err)
			}
		}

		state, err := engine.State(sessionRequest(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		goal, found := findGoal(state.Actors["npc.mira"], "goal.connect")
		if !found ||
			goal.Status != "completed" ||
			!goal.StatusExplicit ||
			goal.StatusUpdatedTick != 15 ||
			goal.StatusSourceEventID != "event.middle-status" ||
			goal.UpdatedTick != 20 ||
			goal.Progress != 5 ||
			goal.ProgressAccumulator != 5 {
			t.Fatalf("arrival order %d produced order-dependent goal status: %+v", index, goal)
		}
	}
}

func TestGoalStatusSameTickUsesStableEventIDTieBreak(t *testing.T) {
	type statusSpec struct {
		name   string
		status string
	}
	alpha := statusSpec{name: "alpha", status: "released"}
	zulu := statusSpec{name: "zulu", status: "completed"}
	for index, order := range [][]statusSpec{{alpha, zulu}, {zulu, alpha}} {
		sessionID := "session.goal-status-tie-" + string(rune('a'+index))
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
			t.Fatal(err)
		}
		proposals := make(map[string]protocol.ActionProposal, 2)
		for _, spec := range []statusSpec{alpha, zulu} {
			proposal, _, err := engine.Propose(
				context.Background(),
				proposeRequest(sessionID, "propose.tie-"+spec.name, 10, nil),
			)
			if err != nil {
				t.Fatal(err)
			}
			proposals[spec.name] = proposal
		}
		for _, spec := range order {
			report := successfulReportRequest(
				proposals[spec.name],
				"report.tie-"+spec.name,
				"event.tie-"+spec.name,
				10,
				"The game applied "+spec.name+".",
			)
			report.Report.GoalUpdates = []protocol.GoalUpdate{{
				GoalID: "goal.connect", Status: spec.status,
			}}
			if _, err := engine.ReportAction(report); err != nil {
				t.Fatal(err)
			}
		}
		state, err := engine.State(sessionRequest(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		goal, found := findGoal(state.Actors["npc.mira"], "goal.connect")
		if !found ||
			goal.Status != "completed" ||
			goal.StatusSourceEventID != "event.tie-zulu" ||
			goal.StatusUpdatedTick != 10 {
			t.Fatalf("same-tick arrival order %d produced unstable status: %+v", index, goal)
		}
	}
}

func TestTickZeroAutomaticGoalStatusDoesNotBecomeExplicitMidReport(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	const sessionID = "session.goal-tick-zero"
	create := createRequest(sessionID)
	create.Actors[0].Goals[0].TargetProgress = 1
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.goal-tick-zero", 0, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	report := successfulReportRequest(
		proposal,
		"report.goal-tick-zero",
		"event.goal-tick-zero",
		0,
		"The game applied and then reversed the progress in one outcome.",
	)
	report.Report.GoalUpdates = []protocol.GoalUpdate{{
		GoalID: "goal.connect", ProgressDelta: -1,
	}}
	if _, err := engine.ReportAction(report); err != nil {
		t.Fatal(err)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	goal, found := findGoal(state.Actors["npc.mira"], "goal.connect")
	if !found ||
		goal.Progress != 0 ||
		goal.ProgressAccumulator != 0 ||
		goal.Status != "active" ||
		goal.StatusExplicit ||
		goal.StatusUpdatedTick != 0 ||
		goal.StatusSourceEventID != "" {
		t.Fatalf("tick-zero automatic status was frozen as explicit: %+v", goal)
	}
}

func TestActionReportEventIDsAreUniqueAcrossMutationKinds(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	const sessionID = "session.event-id-unique"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(observeRequest(sessionID, "observe.shared", "event.shared", 0)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(context.Background(), proposeRequest(sessionID, "propose.shared", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	report := successfulReportRequest(
		proposal, "report.shared", "event.shared", 0, "This must not be recorded.",
	)
	if _, err := engine.ReportAction(report); !errors.Is(err, rinruntime.ErrConflict) ||
		rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("report reused an observation event id: %v", err)
	}

	rejected, _, err := engine.Propose(context.Background(), proposeRequest(sessionID, "propose.rejected-id", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReportAction(rejectedReportRequest(
		rejected, "report.rejected-id", "event.rejected-id", 0, "The game rejected it.",
	)); err != nil {
		t.Fatal(err)
	}
	duplicateObservation := observeRequest(
		sessionID,
		"observe.reuse-rejected-id",
		"event.rejected-id",
		0,
	)
	if _, err := engine.Observe(duplicateObservation); !errors.Is(err, rinruntime.ErrConflict) ||
		rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("observation reused a rejected outcome event id: %v", err)
	}
	next, _, err := engine.Propose(context.Background(), proposeRequest(sessionID, "propose.after-rejection", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	reused := successfulReportRequest(
		next, "report.reuse-rejected-id", "event.rejected-id", 0, "This must not be recorded.",
	)
	if _, err := engine.ReportAction(reused); !errors.Is(err, rinruntime.ErrConflict) ||
		rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("accepted report reused a rejected report event id: %v", err)
	}
}

func TestBatchReportEventIDsAreUniqueWithinBatchAndAcrossKinds(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	create := twoActorWorldRequest("session.batch-event-id-unique")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	observation := observeRequest(create.SessionID, "observe.batch-shared", "event.batch-shared", 0)
	observation.ObserverIDs = []string{"npc.mira", "npc.oren"}
	if _, err := engine.Observe(observation); err != nil {
		t.Fatal(err)
	}
	mira, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.batch-id-mira", "npc.mira"))
	if err != nil {
		t.Fatal(err)
	}
	oren, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.batch-id-oren", "npc.oren"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	crossKindMira := successfulReportRequest(
		mira, "unused", "event.batch-shared", 0, "Must not report.",
	).Report
	crossKindOren := successfulReportRequest(
		oren, "unused", "event.batch-other", 0, "Must not report.",
	).Report
	if _, err := engine.ReportActionBatch(protocol.BatchActionReportRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "report.batch-cross-kind-id",
		Tick:            0,
		Reports:         []protocol.ActionReport{crossKindMira, crossKindOren},
	}); !errors.Is(err, rinruntime.ErrConflict) || rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("batch reused an observation event id: %v", err)
	}
	duplicateMira := successfulReportRequest(
		mira, "unused", "event.batch-duplicate", 0, "Must not report.",
	).Report
	duplicateOren := successfulReportRequest(
		oren, "unused", "event.batch-duplicate", 0, "Must not report.",
	).Report
	if _, err := engine.ReportActionBatch(protocol.BatchActionReportRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "report.batch-duplicate-id",
		Tick:            0,
		Reports:         []protocol.ActionReport{duplicateMira, duplicateOren},
	}); rinruntime.ErrorCode(err) != "invalid_request" {
		t.Fatalf("batch accepted duplicate item event ids: %v", err)
	}
	afterFailures, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if afterFailures.Revision != before.Revision ||
		afterFailures.Proposals[mira.ID].Status != "pending" ||
		afterFailures.Proposals[oren.ID].Status != "pending" {
		t.Fatalf("invalid batch mutated state: before=%+v after=%+v", before, afterFailures)
	}

	rejectedMira := rejectedReportRequest(
		mira, "unused", "event.batch-rejected", 0, "The game rejected it.",
	).Report
	acceptedOren := successfulReportRequest(
		oren, "unused", "event.batch-accepted", 0, "The game applied it.",
	).Report
	if _, err := engine.ReportActionBatch(protocol.BatchActionReportRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "report.batch-record-ids",
		Tick:            0,
		Reports:         []protocol.ActionReport{rejectedMira, acceptedOren},
	}); err != nil {
		t.Fatal(err)
	}
	next, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.after-batch-id", "npc.mira"))
	if err != nil {
		t.Fatal(err)
	}
	reusedBatchID := successfulReportRequest(
		next,
		"report.reuse-batch-rejected",
		"event.batch-rejected",
		0,
		"Must not reuse a rejected batch event id.",
	)
	if _, err := engine.ReportAction(reusedBatchID); !errors.Is(err, rinruntime.ErrConflict) ||
		rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("single report reused a rejected batch event id: %v", err)
	}
}

func TestActionReportRejectsNextThinkTickOverflow(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	const sessionID = "session.tick-overflow"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.max-tick", protocol.MaxJSONSafeInteger-4, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	overflowReport := successfulReportRequest(
		proposal,
		"report.max-tick",
		"event.max-tick",
		protocol.MaxJSONSafeInteger-4,
		"This must not overflow scheduling state.",
	)
	if _, err := engine.ReportAction(overflowReport); !errors.Is(err, rinruntime.ErrConflict) ||
		rinruntime.ErrorCode(err) != "tick_overflow" {
		t.Fatalf("expected tick_overflow, got %v", err)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Proposals[proposal.ID].Status != "pending" || state.Revision != 2 {
		t.Fatalf("overflowing report mutated state: %+v", state)
	}
}

func TestBatchReportRejectsNextThinkTickOverflow(t *testing.T) {
	engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
	create := twoActorWorldRequest("session.batch-tick-overflow")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	request := targetedProposalRequest(create.SessionID, "propose.batch-max-tick", "npc.mira")
	request.Tick = protocol.MaxJSONSafeInteger
	proposal, _, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	overflowBatchReport := successfulReportRequest(
		proposal,
		"unused",
		"event.batch-max-tick",
		protocol.MaxJSONSafeInteger,
		"This must not overflow scheduling state.",
	).Report
	if _, err := engine.ReportActionBatch(protocol.BatchActionReportRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "report.batch-max-tick",
		Tick:            protocol.MaxJSONSafeInteger,
		Reports:         []protocol.ActionReport{overflowBatchReport},
	}); !errors.Is(err, rinruntime.ErrConflict) || rinruntime.ErrorCode(err) != "tick_overflow" {
		t.Fatalf("expected batch tick_overflow, got %v", err)
	}
	state, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Proposals[proposal.ID].Status != "pending" || state.Revision != 2 {
		t.Fatalf("overflowing batch report mutated state: %+v", state)
	}
}

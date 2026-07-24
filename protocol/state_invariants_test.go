package protocol

import (
	"strings"
	"testing"
)

func TestSessionStateProposalGenerationInvariants(t *testing.T) {
	state := invariantTestState()
	proposal := invariantTestProposal(state, "proposal.general", "pending")
	state.Proposals[proposal.ID] = proposal
	requireValidState(t, state)

	tests := []struct {
		name   string
		mutate func(*ActionProposal)
	}{
		{
			name: "base equals creation",
			mutate: func(proposal *ActionProposal) {
				proposal.BasedOnRevision = proposal.CreatedRevision
			},
		},
		{
			name: "zero base outside fresh restore",
			mutate: func(proposal *ActionProposal) {
				proposal.BasedOnRevision = 0
				proposal.BasedOnHeadHash = ""
			},
		},
		{
			name: "missing hash for positive base",
			mutate: func(proposal *ActionProposal) {
				proposal.BasedOnHeadHash = ""
			},
		},
		{
			name: "creation exceeds state",
			mutate: func(proposal *ActionProposal) {
				proposal.CreatedRevision = state.Revision + 1
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			invalid := state
			invalid.Proposals = make(map[string]ActionProposal, len(state.Proposals))
			for id, proposal := range state.Proposals {
				testCase.mutate(&proposal)
				invalid.Proposals[id] = proposal
			}
			requireInvalidState(t, invalid)
		})
	}

	fresh := invariantTestState()
	freshProposal := invariantTestProposal(fresh, "proposal.fresh", "pending")
	freshProposal.CreatedRevision = 1
	freshProposal.BasedOnRevision = 0
	freshProposal.BasedOnHeadHash = ""
	fresh.Proposals[freshProposal.ID] = freshProposal
	requireValidState(t, fresh)

	freshProposal.BasedOnHeadHash = invariantTestHash()
	fresh.Proposals[freshProposal.ID] = freshProposal
	requireInvalidState(t, fresh)
}

func TestSessionStateProposalBoundaryAuditReferenceMustExist(t *testing.T) {
	state := invariantTestState()
	proposal := invariantTestProposal(state, "proposal.boundary", "pending")
	proposal.BoundaryID = "boundary.test"
	state.Proposals[proposal.ID] = proposal
	requireValidState(t, state)

	proposal.BoundaryID = "boundary.unknown"
	state.Proposals[proposal.ID] = proposal
	requireInvalidState(t, state)
}

func TestSessionStateRecalledIDsOnlyReferenceLiveMemoryEntities(t *testing.T) {
	state := invariantTestState(FeatureMemoryArchive)
	actor := state.Actors["npc.test"]
	actor.MemorySummaries = []MemorySummary{
		invariantTestSummary("summary.live", "memory.archived", "event.archived"),
		invariantTestSummary("summary.other", "memory.archived", "event.archived"),
	}
	state.Actors[actor.ID] = actor
	proposal := invariantTestProposal(state, "proposal.memory", "pending")
	proposal.RecalledMemoryIDs = []string{"summary.live"}
	state.Proposals[proposal.ID] = proposal

	// Lineage is local metadata on each summary. It may overlap across
	// summaries, but it is not itself a recallable memory entity.
	requireValidState(t, state)

	proposal.RecalledMemoryIDs = []string{"memory.archived"}
	state.Proposals[proposal.ID] = proposal
	requireInvalidState(t, state)

	proposal.RecalledMemoryIDs = []string{"summary.live"}
	state.Proposals[proposal.ID] = proposal
	actor = state.Actors["npc.test"]
	actor.MemorySummaries[0].SourceMemoryIDs = []string{"memory.archived", "memory.archived"}
	state.Actors[actor.ID] = actor
	requireInvalidState(t, state)
}

func TestSessionStatePendingProposedGoalReservations(t *testing.T) {
	t.Run("unique pending reservation", func(t *testing.T) {
		state := invariantTestState(FeatureGoalCandidates)
		proposal := invariantTestProposal(state, "proposal.goal.one", "pending")
		proposal.GoalID = "goal.candidate"
		proposal.ProposedGoal = invariantTestCandidateGoal("goal.candidate")
		state.Proposals[proposal.ID] = proposal
		requireValidState(t, state)
	})

	t.Run("pending reservations cannot overlap", func(t *testing.T) {
		state := invariantTestState(FeatureGoalCandidates)
		for _, id := range []string{"proposal.goal.one", "proposal.goal.two"} {
			proposal := invariantTestProposal(state, id, "pending")
			proposal.GoalID = "goal.candidate"
			proposal.ProposedGoal = invariantTestCandidateGoal("goal.candidate")
			state.Proposals[proposal.ID] = proposal
		}
		requireInvalidState(t, state)
	})

	t.Run("pending reservation cannot reuse actor goal", func(t *testing.T) {
		state := invariantTestState(FeatureGoalCandidates)
		proposal := invariantTestProposal(state, "proposal.goal.existing", "pending")
		proposal.GoalID = "goal.test"
		proposal.ProposedGoal = invariantTestCandidateGoal("goal.test")
		state.Proposals[proposal.ID] = proposal
		requireInvalidState(t, state)
	})

	t.Run("pending reservations count against capacity", func(t *testing.T) {
		state := invariantTestState(FeatureGoalCandidates)
		actor := state.Actors["npc.test"]
		actor.Goals = make([]Goal, 32)
		for index := range actor.Goals {
			actor.Goals[index] = *invariantTestCandidateGoal("goal." + testIndex(index))
		}
		state.Actors[actor.ID] = actor
		proposal := invariantTestProposal(state, "proposal.goal.overflow", "pending")
		proposal.GoalID = "goal.overflow"
		proposal.ProposedGoal = invariantTestCandidateGoal("goal.overflow")
		state.Proposals[proposal.ID] = proposal
		requireInvalidState(t, state)
	})

	t.Run("resolved proposal may retain proposed goal", func(t *testing.T) {
		state := invariantTestState(FeatureGoalCandidates)
		accepted := invariantTestProposal(state, "proposal.goal.accepted", "accepted")
		accepted.GoalID = "goal.test"
		accepted.ProposedGoal = invariantTestCandidateGoal("goal.test")
		state.Proposals[accepted.ID] = accepted
		rejected := invariantTestProposal(state, "proposal.goal.rejected", "rejected")
		rejected.GoalID = "goal.rejected"
		rejected.ProposedGoal = invariantTestCandidateGoal("goal.rejected")
		state.Proposals[rejected.ID] = rejected
		requireValidState(t, state)
	})

	t.Run("accepted proposed goal must enter actor state", func(t *testing.T) {
		state := invariantTestState(FeatureGoalCandidates)
		accepted := invariantTestProposal(state, "proposal.goal.missing", "accepted")
		accepted.GoalID = "goal.missing"
		accepted.ProposedGoal = invariantTestCandidateGoal("goal.missing")
		state.Proposals[accepted.ID] = accepted
		requireInvalidState(t, state)

		delete(state.Proposals, accepted.ID)
		actor := state.Actors["npc.test"]
		actor.RecentActions = []ActionProposal{accepted}
		state.Actors[actor.ID] = actor
		requireInvalidState(t, state)
	})
}

func TestSessionStateBeliefClosureAndSemanticVisibility(t *testing.T) {
	state := invariantTestState(FeatureBeliefConflicts)
	actor := state.Actors["npc.test"]
	selected := Fact{
		SubjectID:     "world.door",
		Predicate:     "state",
		Object:        "open",
		Confidence:    100,
		SourceEventID: "event.door",
	}
	claim := selected
	claim.Visibility = []string{}
	key := selected.SubjectID + ":" + selected.Predicate
	actor.Beliefs[key] = selected
	actor.BeliefSets = map[string]BeliefSet{
		key: {
			SubjectID:             selected.SubjectID,
			Predicate:             selected.Predicate,
			Claims:                []BeliefClaim{{Fact: claim, ObservedRevision: 2}},
			SelectedSourceEventID: selected.SourceEventID,
		},
	}
	state.Actors[actor.ID] = actor
	requireValidState(t, state)

	baseActor := actor
	missingSet := state
	missingSet.Actors = make(map[string]ActorState, 1)
	missingActor := baseActor
	missingActor.BeliefSets = map[string]BeliefSet{}
	missingSet.Actors[missingActor.ID] = missingActor
	requireInvalidState(t, missingSet)

	unknownVisibility := state
	unknownVisibility.Actors = make(map[string]ActorState, 1)
	unknownActor := baseActor
	set := unknownActor.BeliefSets[key]
	set.Claims = append([]BeliefClaim(nil), set.Claims...)
	set.Claims[0].Fact.Visibility = []string{"npc.unknown"}
	unknownActor.BeliefSets = map[string]BeliefSet{key: set}
	unknownVisibility.Actors[unknownActor.ID] = unknownActor
	requireInvalidState(t, unknownVisibility)
}

func TestSessionStateGoalAndTemporalBounds(t *testing.T) {
	state := invariantTestState(
		FeatureOutcomeReporting,
		FeatureMemoryArchive,
		FeatureBeliefConflicts,
		FeatureActorActivity,
	)
	actor := state.Actors["npc.test"]
	actor.Memories = []Memory{{
		ID: "memory.test", EventID: "event.memory", Tick: 1,
		Summary: "A memory.", Importance: 1, CreatedRevision: 2,
	}}
	actor.MemorySummaries = []MemorySummary{
		invariantTestSummary("summary.test", "memory.old", "event.old"),
	}
	actor.Activity = &ActorActivity{State: "awake", UpdatedTick: 1, UpdatedRevision: 2}
	fact := Fact{
		SubjectID: "world.door", Predicate: "state", Object: "open",
		Confidence: 100, SourceEventID: "event.fact", ObservedTick: 1,
	}
	key := fact.SubjectID + ":" + fact.Predicate
	actor.Beliefs[key] = fact
	actor.BeliefSets = map[string]BeliefSet{
		key: {
			SubjectID: fact.SubjectID, Predicate: fact.Predicate,
			Claims:                []BeliefClaim{{Fact: fact, ObservedRevision: 2}},
			SelectedSourceEventID: fact.SourceEventID,
		},
	}
	state.Actors[actor.ID] = actor
	state.Receipts["request.legacy"] = RequestReceipt{Kind: "observation.recorded", Revision: 0}
	requireValidState(t, state)

	tests := []struct {
		name   string
		mutate func(*SessionState)
	}{
		{
			name: "memory future revision",
			mutate: func(state *SessionState) {
				actor := state.Actors["npc.test"]
				actor.Memories[0].CreatedRevision = state.Revision + 1
				state.Actors[actor.ID] = actor
			},
		},
		{
			name: "summary future tick",
			mutate: func(state *SessionState) {
				actor := state.Actors["npc.test"]
				actor.MemorySummaries[0].EndTick = state.Tick + 1
				state.Actors[actor.ID] = actor
			},
		},
		{
			name: "claim future revision",
			mutate: func(state *SessionState) {
				actor := state.Actors["npc.test"]
				set := actor.BeliefSets[key]
				set.Claims[0].ObservedRevision = state.Revision + 1
				actor.BeliefSets[key] = set
				state.Actors[actor.ID] = actor
			},
		},
		{
			name: "activity future revision",
			mutate: func(state *SessionState) {
				actor := state.Actors["npc.test"]
				activity := *actor.Activity
				activity.UpdatedRevision = state.Revision + 1
				actor.Activity = &activity
				state.Actors[actor.ID] = actor
			},
		},
		{
			name: "receipt future revision",
			mutate: func(state *SessionState) {
				state.Receipts["request.future"] = RequestReceipt{
					Kind: "observation.recorded", Revision: state.Revision + 1,
				}
			},
		},
		{
			name: "missing current receipt",
			mutate: func(state *SessionState) {
				delete(state.Receipts, "request.current")
			},
		},
		{
			name: "current receipt downgraded to historical",
			mutate: func(state *SessionState) {
				receipt := state.Receipts["request.current"]
				receipt.Revision = 0
				state.Receipts["request.current"] = receipt
			},
		},
		{
			name: "duplicate current receipt revision",
			mutate: func(state *SessionState) {
				state.Receipts["request.duplicate-current"] = RequestReceipt{
					Kind: "observation.recorded", Revision: state.Revision,
				}
			},
		},
		{
			name: "duplicate historical non-zero receipt revision",
			mutate: func(state *SessionState) {
				state.Receipts["request.historical-a"] = RequestReceipt{
					Kind: "observation.recorded", Revision: 2,
				}
				state.Receipts["request.historical-b"] = RequestReceipt{
					Kind: "observation.recorded", Revision: 2,
				}
			},
		},
		{
			name: "automatic status disagrees with progress",
			mutate: func(state *SessionState) {
				actor := state.Actors["npc.test"]
				actor.Goals[0].Status = "released"
				state.Actors[actor.ID] = actor
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			invalid := invariantTestState(
				FeatureOutcomeReporting,
				FeatureMemoryArchive,
				FeatureBeliefConflicts,
				FeatureActorActivity,
			)
			invalidActor := invalid.Actors["npc.test"]
			invalidActor.Memories = append([]Memory(nil), actor.Memories...)
			invalidActor.MemorySummaries = append([]MemorySummary(nil), actor.MemorySummaries...)
			invalidActor.Activity = actor.Activity
			invalidActor.Beliefs = map[string]Fact{key: fact}
			set := actor.BeliefSets[key]
			set.Claims = append([]BeliefClaim(nil), set.Claims...)
			invalidActor.BeliefSets = map[string]BeliefSet{key: set}
			invalid.Actors[invalidActor.ID] = invalidActor
			invalid.Receipts["request.legacy"] = RequestReceipt{Kind: "observation.recorded", Revision: 0}
			testCase.mutate(&invalid)
			requireInvalidState(t, invalid)
		})
	}
}

func TestSessionStateReceiptKindsMatchDurableMutations(t *testing.T) {
	valid := []string{
		identifierHistoryCreateKind,
		identifierHistoryObservationKind,
		identifierHistoryProposalKind,
		identifierHistoryCommitKind,
		identifierHistoryBatchKind,
		identifierHistoryActivityKind,
		identifierHistoryArbitrationKind,
		identifierHistoryRestoreKind,
	}
	for _, kind := range valid {
		t.Run(kind, func(t *testing.T) {
			state := invariantTestState()
			receipt := state.Receipts["request.current"]
			receipt.Kind = kind
			state.Receipts["request.current"] = receipt
			requireValidState(t, state)
		})
	}

	state := invariantTestState()
	receipt := state.Receipts["request.current"]
	receipt.Kind = "custom.mutation"
	state.Receipts["request.current"] = receipt
	requireInvalidState(t, state)
}

func TestSessionStateArbitrationDisabledRequiresZeroWorldBases(t *testing.T) {
	state := invariantTestState()
	state.WorldRevision = 1
	requireInvalidState(t, state)

	state = invariantTestState()
	proposal := invariantTestProposal(state, "proposal.world", "pending")
	proposal.BasedOnWorldRevision = 1
	state.Proposals[proposal.ID] = proposal
	requireInvalidState(t, state)
}

func TestSessionStateArbitrationTemporalBounds(t *testing.T) {
	state := invariantTestState(FeatureArbitration)
	state.Arbitrations = []ArbitrationRecord{{
		ID: "arbitration.test", RequestID: "request.arbitration",
		Tick: 20, BasedOnWorldRevision: 2, CreatedRevision: 3,
		Decisions: []ArbitrationDecision{{
			ProposalID: "proposal.selected", ActorID: "npc.test",
			Status: "selected", Reason: "No conflicting target.",
		}},
	}}
	requireValidState(t, state)

	state.Arbitrations[0].CreatedRevision = state.Revision + 1
	requireInvalidState(t, state)
}

func TestActorSeedsRequireUniqueBoundaryAndGoalIDs(t *testing.T) {
	request := CreateSessionRequest{
		ProtocolVersion: Version,
		RequestID:       "create.unique",
		SessionID:       "session.unique",
		Binding:         invariantTestBinding(),
		Actors: []ActorSeed{{
			ID: "npc.test", Kind: "npc", DisplayName: "Test",
			ThinkEveryTicks: 1, Enabled: true,
			Boundaries: []Boundary{
				{ID: "boundary.same", Description: "First.", Response: "refuse"},
				{ID: "boundary.same", Description: "Second.", Response: "wait"},
			},
		}},
	}
	if err := ValidateCreateSession(request); err == nil {
		t.Fatal("duplicate boundary ids should fail")
	}

	request.Actors[0].Boundaries = nil
	request.Actors[0].Goals = []Goal{
		{ID: "goal.same", Description: "First.", Priority: 1, TargetProgress: 1, Status: "active"},
		{ID: "goal.same", Description: "Second.", Priority: 2, TargetProgress: 1, Status: "active"},
	}
	if err := ValidateCreateSession(request); err == nil {
		t.Fatal("duplicate goal ids should fail")
	}
}

func TestCreateSessionOutcomeGoalStatusStartsCoherently(t *testing.T) {
	request := CreateSessionRequest{
		ProtocolVersion: Version,
		RequestID:       "create.test",
		SessionID:       "session.test",
		Binding:         invariantTestBinding(),
		Features:        []string{FeatureOutcomeReporting},
		Actors: []ActorSeed{{
			ID: "npc.test", Kind: "npc", DisplayName: "Test",
			ThinkEveryTicks: 1, Enabled: true,
			Goals: []Goal{{
				ID: "goal.test", Description: "Test goal.", Priority: 1,
				Progress: 1, TargetProgress: 1, Status: "active",
			}},
		}},
	}
	if err := ValidateCreateSession(request); err == nil {
		t.Fatal("completed progress with automatic active status should fail")
	}
	request.Actors[0].Goals[0].Status = "completed"
	if err := ValidateCreateSession(request); err != nil {
		t.Fatalf("explicit initial completed status should pass: %v", err)
	}
}

func invariantTestState(features ...string) SessionState {
	state := SessionState{
		ProtocolVersion: Version,
		SessionID:       "session.test",
		Binding:         invariantTestBinding(),
		Features:        append([]string(nil), features...),
		Tick:            10,
		Revision:        3,
		HeadHash:        invariantTestHash(),
		Actors: map[string]ActorState{
			"npc.test": {
				ActorSeed: ActorSeed{
					ID: "npc.test", Kind: "npc", DisplayName: "Test",
					ThinkEveryTicks: 1, Enabled: true,
					Boundaries: []Boundary{{
						ID: "boundary.test", Description: "Test boundary.",
						TriggerTags: []string{"private"}, Response: "refuse",
					}},
					Goals: []Goal{{
						ID: "goal.test", Description: "Test goal.", Priority: 1,
						TargetProgress: 10, Status: "active",
					}},
				},
				Beliefs: make(map[string]Fact),
			},
		},
		Proposals: make(map[string]ActionProposal),
		Receipts: map[string]RequestReceipt{
			"request.current": {
				Kind:     "session.created",
				EntityID: "session.test",
				Revision: 3,
			},
		},
	}
	if HasFeature(features, FeatureArbitration) {
		state.WorldRevision = 2
	}
	return state
}

func invariantTestProposal(state SessionState, id, status string) ActionProposal {
	proposal := ActionProposal{
		ID:              id,
		SessionID:       state.SessionID,
		RequestID:       "request." + id,
		ActorID:         "npc.test",
		Tick:            1,
		BasedOnRevision: 2,
		BasedOnHeadHash: invariantTestHash(),
		CreatedRevision: 3,
		Action: ActionSpec{
			ID: "action.wait", Kind: "wait", Description: "Wait.",
		},
		Stance:    "wait",
		Summary:   "Wait.",
		Rationale: "A deterministic test action.",
		Status:    status,
	}
	if HasFeature(state.Features, FeatureArbitration) {
		proposal.BasedOnWorldRevision = state.WorldRevision
	}
	if status != "pending" && HasFeature(state.Features, FeatureOutcomeReporting) {
		proposal.OutcomeEventID = "event.outcome." + id
		proposal.OutcomeTick = 2
	}
	return proposal
}

func invariantTestCandidateGoal(id string) *Goal {
	return &Goal{
		ID: id, Description: "Candidate goal.", Priority: 1,
		TargetProgress: 10, Status: "active",
	}
}

func invariantTestSummary(id, sourceMemoryID, sourceEventID string) MemorySummary {
	return MemorySummary{
		ID: id, Level: 1, Summary: "A compacted memory.",
		SourceMemoryIDs: []string{sourceMemoryID},
		SourceEventIDs:  []string{sourceEventID},
		StartTick:       1, EndTick: 2, Importance: 1,
		Reason: "episodic_capacity", CreatedRevision: 2,
	}
}

func invariantTestBinding() Binding {
	return Binding{
		GameID: "game.test", ContentID: "base",
		ContentVersion: "1", ContentHash: "hash",
	}
}

func invariantTestHash() string {
	return strings.Repeat("a", 64)
}

func testIndex(index int) string {
	const digits = "0123456789"
	return string([]byte{
		digits[(index/10)%10],
		digits[index%10],
	})
}

func requireValidState(t *testing.T, state SessionState) {
	t.Helper()
	if err := ValidateSessionState(state); err != nil {
		t.Fatalf("state should be valid: %v", err)
	}
}

func requireInvalidState(t *testing.T, state SessionState) {
	t.Helper()
	if err := ValidateSessionState(state); err == nil {
		t.Fatal("state should be invalid")
	}
}

package protocol

import (
	"errors"
	"testing"
)

func TestRequestJSONSafeIntegerBoundaries(t *testing.T) {
	maximum := int64(MaxJSONSafeInteger)
	baseCreate := CreateSessionRequest{
		ProtocolVersion: Version,
		RequestID:       "request.safe-integer",
		SessionID:       "session.safe-integer",
		Binding: Binding{
			GameID: "game.test", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Actors: []ActorSeed{{
			ID: "npc.test", Kind: "npc", DisplayName: "Test", ThinkEveryTicks: 1, Enabled: true,
		}},
	}
	for _, seed := range []int64{-maximum, maximum} {
		request := baseCreate
		request.Seed = seed
		if err := ValidateCreateSession(request); err != nil {
			t.Fatalf("seed %d rejected: %v", seed, err)
		}
	}
	for _, seed := range []int64{-maximum - 1, maximum + 1} {
		request := baseCreate
		request.Seed = seed
		requireValidationField(t, ValidateCreateSession(request), "seed")
	}

	tests := []struct {
		name     string
		validate func(int64) error
	}{
		{"observe", func(tick int64) error {
			return ValidateObserve(ObserveRequest{
				ProtocolVersion: Version, SessionID: "session.test", RequestID: "request.observe",
				EventID: "event.observe", Tick: tick, ObserverIDs: []string{"npc.test"},
				Source: "game", Kind: "world", Summary: "Observed.", Importance: 1,
			})
		}},
		{"propose", func(tick int64) error {
			return ValidatePropose(ProposeRequest{
				ProtocolVersion: Version, SessionID: "session.test", RequestID: "request.propose",
				ActorID: "npc.test", Tick: tick, Intent: "Act.",
				CandidateActions: []ActionSpec{{ID: "action.wait", Kind: "wait", Description: "Wait."}},
			})
		}},
		{"commit", func(tick int64) error {
			return ValidateCommit(CommitRequest{
				ProtocolVersion: Version, SessionID: "session.test", RequestID: "request.commit",
				ProposalID: "proposal.test", EventID: "event.commit", Tick: tick,
			})
		}},
		{"batch", func(tick int64) error {
			return ValidateBatchCommit(BatchCommitRequest{
				ProtocolVersion: Version, SessionID: "session.test", RequestID: "request.batch",
				Tick: tick, Items: []CommitItem{{
					ProposalID: "proposal.test", EventID: "event.batch", Accepted: false,
				}},
			})
		}},
		{"activity", func(tick int64) error {
			return ValidateSetActorActivity(SetActorActivityRequest{
				ProtocolVersion: Version, SessionID: "session.test", RequestID: "request.activity",
				Tick: tick, Updates: []ActorActivityUpdate{{ActorID: "npc.test", State: "awake"}},
			})
		}},
		{"arbitrate", func(tick int64) error {
			return ValidateArbitrate(ArbitrateRequest{
				ProtocolVersion: Version, SessionID: "session.test", RequestID: "request.arbitrate",
				Tick: tick, ProposalIDs: []string{"proposal.test"},
			})
		}},
		{"due", func(tick int64) error {
			return ValidateDueAgents(DueAgentsRequest{
				ProtocolVersion: Version, SessionID: "session.test", Tick: tick, Limit: 1,
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(maximum); err != nil {
				t.Fatalf("maximum rejected: %v", err)
			}
			requireValidationField(t, test.validate(maximum+1), "tick")
		})
	}

	if err := ValidateTimeline(TimelineRequest{
		ProtocolVersion: Version, SessionID: "session.test",
		AfterRevision: uint64(maximum), Limit: 1,
	}); err != nil {
		t.Fatalf("maximum timeline revision rejected: %v", err)
	}
	requireValidationField(t, ValidateTimeline(TimelineRequest{
		ProtocolVersion: Version, SessionID: "session.test",
		AfterRevision: uint64(maximum) + 1, Limit: 1,
	}), "after_revision")
	if err := ValidateReplay(ReplayRequest{
		ProtocolVersion: Version, SessionID: "session.test", Revision: uint64(maximum),
	}); err != nil {
		t.Fatalf("maximum replay revision rejected: %v", err)
	}
	requireValidationField(t, ValidateReplay(ReplayRequest{
		ProtocolVersion: Version, SessionID: "session.test", Revision: uint64(maximum) + 1,
	}), "revision")
}

func TestImportedStateAndIdentifierHistoryRejectUnsafeIntegers(t *testing.T) {
	maximum := int64(MaxJSONSafeInteger)
	state := invariantTestState()
	state.Seed = maximum
	state.Tick = maximum
	state.Actors["npc.test"] = func() ActorState {
		actor := state.Actors["npc.test"]
		actor.NextThinkTick = maximum
		return actor
	}()
	if err := ValidateSessionState(state); err != nil {
		t.Fatalf("safe maxima rejected: %v", err)
	}

	tests := []struct {
		name  string
		field string
		apply func(*SessionState)
	}{
		{"seed", "state.seed", func(value *SessionState) { value.Seed = maximum + 1 }},
		{"tick", "state.tick", func(value *SessionState) { value.Tick = maximum + 1 }},
		{"revision", "state.revision", func(value *SessionState) {
			value.Revision = uint64(maximum) + 1
		}},
		{"next think", "state.actors.npc.test.next_think_tick", func(value *SessionState) {
			actor := value.Actors["npc.test"]
			actor.NextThinkTick = maximum + 1
			value.Actors["npc.test"] = actor
		}},
		{"goal accumulator", "state.actors.npc.test.goals[0].progress_accumulator", func(value *SessionState) {
			actor := value.Actors["npc.test"]
			actor.Goals[0].ProgressAccumulator = maximum + 1
			value.Actors["npc.test"] = actor
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := invariantTestState()
			test.apply(&candidate)
			requireValidationField(t, ValidateSessionState(candidate), test.field)
		})
	}

	history := IdentifierHistory{
		Version:          IdentifierHistoryVersion,
		CoverageComplete: true,
		Requests: map[string]RequestIdentity{
			"request.test": {
				Kind: "session.created", ResultRevision: uint64(maximum) + 1,
			},
		},
	}
	requireValidationField(
		t,
		ValidateIdentifierHistory(history, "session.test"),
		"identifier_history.requests.request.test.result_revision",
	)
	history.Requests = nil
	history.Events = map[string]EventIdentity{
		"event.test": {Kind: "observation.recorded", Revision: uint64(maximum) + 1},
	}
	requireValidationField(
		t,
		ValidateIdentifierHistory(history, "session.test"),
		"identifier_history.events.event.test.revision",
	)
}

func requireValidationField(t *testing.T, err error, field string) {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error=%v, want validation error for %s", err, field)
	}
	if validation.Field != field {
		t.Fatalf("validation field=%q, want %q", validation.Field, field)
	}
}

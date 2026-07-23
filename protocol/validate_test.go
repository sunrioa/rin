package protocol_test

import (
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestCreateValidationRejectsUnsafeAndDuplicateActors(t *testing.T) {
	request := validCreate()
	request.SessionID = "../escape"
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("unsafe session id should fail")
	}
	request = validCreate()
	request.Actors = append(request.Actors, request.Actors[0])
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("duplicate actors should fail")
	}
}

func TestCreateValidationRejectsInvalidBoundaryAndProtocol(t *testing.T) {
	request := validCreate()
	request.ProtocolVersion = "rin.protocol/v2"
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("unsupported protocol should fail")
	}
	request = validCreate()
	request.Actors[0].Boundaries[0].Response = "execute"
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("unsafe boundary response should fail")
	}
}

func TestCreateValidationNegotiatesKnownFeatures(t *testing.T) {
	request := validCreate()
	request.Features = []string{protocol.FeatureMemoryArchive, protocol.FeatureBeliefConflicts}
	if err := protocol.ValidateCreateSession(request); err != nil {
		t.Fatalf("known features should validate: %v", err)
	}
	request.Features = append(request.Features, "future-untrusted-feature")
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("unknown feature should fail")
	}
	request.Features = []string{protocol.FeatureMemoryArchive, protocol.FeatureMemoryArchive}
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("duplicate feature should fail")
	}
}

func TestOccurrenceMetadataIsServerOwnedAndNonNegative(t *testing.T) {
	create := validCreate()
	create.Actors[0].Goals[0].UpdatedTick = 1
	if err := protocol.ValidateCreateSession(create); err == nil {
		t.Fatal("create request supplied server-owned goal updated_tick")
	}
	create = validCreate()
	create.Actors[0].Goals[0].ProgressAccumulator = 1
	if err := protocol.ValidateCreateSession(create); err == nil {
		t.Fatal("create request supplied server-owned progress_accumulator")
	}
	create = validCreate()
	create.Actors[0].Goals[0].StatusExplicit = true
	if err := protocol.ValidateCreateSession(create); err == nil {
		t.Fatal("create request supplied server-owned status_explicit")
	}

	proposal := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.test",
		RequestID:       "proposal.metadata",
		ActorID:         "npc.test",
		Intent:          "choose",
		CandidateActions: []protocol.ActionSpec{{
			ID: "wait", Kind: "wait", Description: "wait",
		}},
		CandidateGoals: []protocol.Goal{{
			ID: "goal.new", Description: "A bounded goal", Priority: 3,
			TargetProgress: 2, Status: "active", UpdatedTick: 1,
		}},
	}
	if err := protocol.ValidatePropose(proposal); err == nil {
		t.Fatal("candidate goal supplied server-owned updated_tick")
	}

	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.test",
		RequestID:       "commit.metadata",
		ProposalID:      "proposal.test",
		EventID:         "event.test",
		Accepted:        true,
		Outcome:         "Applied.",
		Facts: []protocol.Fact{{
			SubjectID: "door", Predicate: "state", Object: "open",
			Confidence: 100, ObservedTick: -1,
		}},
	}
	if err := protocol.ValidateCommit(commit); err == nil {
		t.Fatal("negative fact observed_tick should fail")
	}

	serverStampedFact := protocol.Fact{
		SubjectID: "door", Predicate: "state", Object: "open",
		Confidence: 100, ObservedTick: 7,
	}
	requests := map[string]func() error{
		"observe": func() error {
			return protocol.ValidateObserve(protocol.ObserveRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.test",
				RequestID:       "observe.metadata",
				EventID:         "event.metadata",
				ObserverIDs:     []string{"npc.test"},
				Source:          "game",
				Kind:            "world",
				Summary:         "The door opened.",
				Importance:      1,
				Facts:           []protocol.Fact{serverStampedFact},
			})
		},
		"commit": func() error {
			return protocol.ValidateCommit(protocol.CommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.test",
				RequestID:       "commit.metadata-positive",
				ProposalID:      "proposal.test",
				EventID:         "event.metadata-positive",
				Accepted:        true,
				Outcome:         "Applied.",
				Facts:           []protocol.Fact{serverStampedFact},
			})
		},
		"batch": func() error {
			return protocol.ValidateBatchCommit(protocol.BatchCommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.test",
				RequestID:       "batch.metadata",
				Items: []protocol.CommitItem{{
					ProposalID: "proposal.test",
					EventID:    "event.metadata-batch",
					Accepted:   true,
					Outcome:    "Applied.",
					Facts:      []protocol.Fact{serverStampedFact},
				}},
			})
		},
	}
	for name, validate := range requests {
		t.Run(name, func(t *testing.T) {
			if err := validate(); err == nil {
				t.Fatal("request supplied server-owned positive observed_tick")
			}
		})
	}
}

func TestProposalRequiresUniqueWhitelistedShape(t *testing.T) {
	request := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.test",
		RequestID:       "request.test",
		ActorID:         "npc.test",
		Intent:          "respond",
		CandidateActions: []protocol.ActionSpec{
			{ID: "talk", Kind: "dialogue", Description: "talk"},
			{ID: "talk", Kind: "wait", Description: "wait"},
		},
	}
	if err := protocol.ValidatePropose(request); err == nil {
		t.Fatal("duplicate action ids should fail")
	}
}

func TestLivingWorldRequestValidation(t *testing.T) {
	proposal := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.test", RequestID: "proposal.test", ActorID: "npc.test",
		Intent: "choose", CandidateActions: []protocol.ActionSpec{{ID: "wait", Kind: "wait", Description: "wait"}},
		CandidateGoals: []protocol.Goal{{ID: "goal.new", Description: "A bounded goal", Priority: 3, TargetProgress: 2, Status: "active"}},
	}
	if err := protocol.ValidatePropose(proposal); err != nil {
		t.Fatalf("valid candidate goal should pass: %v", err)
	}
	proposal.CandidateGoals[0].Progress = 1
	if err := protocol.ValidatePropose(proposal); err == nil {
		t.Fatal("candidate goal with progress should fail")
	}
	activity := protocol.SetActorActivityRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.test", RequestID: "activity.test", Tick: 1,
		Updates: []protocol.ActorActivityUpdate{{ActorID: "npc.test", RegionID: "region.test", State: "sleeping"}},
	}
	if err := protocol.ValidateSetActorActivity(activity); err == nil {
		t.Fatal("unknown activity state should fail")
	}
	batch := protocol.BatchCommitRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.test", RequestID: "batch.test",
		Items: []protocol.CommitItem{{ProposalID: "proposal.one", EventID: "event.one", Accepted: true}},
	}
	if err := protocol.ValidateBatchCommit(batch); err == nil {
		t.Fatal("accepted batch item without outcome should fail")
	}
}

func validCreate() protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.test",
		SessionID:       "session.test",
		Binding:         protocol.Binding{GameID: "game.test", ContentID: "base", ContentVersion: "1", ContentHash: "hash"},
		Actors: []protocol.ActorSeed{{
			ID: "npc.test", Kind: "npc", DisplayName: "Test", ThinkEveryTicks: 1, Enabled: true,
			Boundaries: []protocol.Boundary{{ID: "boundary.test", Description: "A boundary", TriggerTags: []string{"private"}, Response: "refuse"}},
			Goals:      []protocol.Goal{{ID: "goal.test", Description: "A goal", Priority: 1, TargetProgress: 1, Status: "active"}},
		}},
	}
}

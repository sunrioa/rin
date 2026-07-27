package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/host"
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
	request.ProtocolVersion = "rin.protocol/v1"
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
	request.Features = []string{
		protocol.FeatureMemoryArchive,
		protocol.FeatureBeliefConflicts,
	}
	if err := protocol.ValidateCreateSession(request); err != nil {
		t.Fatalf("known features should validate: %v", err)
	}
	request.Features = append(request.Features, "future-untrusted-feature")
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("unknown feature should fail")
	}
	request.Features = []string{
		protocol.FeatureMemoryArchive,
		protocol.FeatureMemoryArchive,
	}
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("duplicate feature should fail")
	}
}

func TestLegacyOutcomeFeatureIsRemoved(t *testing.T) {
	request := validCreate()
	request.Features = []string{"outcome-reporting-v1"}
	if err := protocol.ValidateCreateSession(request); err == nil {
		t.Fatal("removed v1 outcome feature was accepted")
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
		DecisionWindow:  validWindow(),
		Offers:          []protocol.ActionOffer{validOffer("wait")},
		CandidateGoals: []protocol.Goal{{
			ID: "goal.new", Description: "A bounded goal", Priority: 3,
			TargetProgress: 2, Status: "active", UpdatedTick: 1,
		}},
	}
	if err := protocol.ValidatePropose(proposal); err == nil {
		t.Fatal("candidate goal supplied server-owned updated_tick")
	}

	report := validTerminalReport("report.metadata", "event.test", []protocol.Fact{{
		SubjectID: "door", Predicate: "state", Object: "open",
		Confidence: 100, ObservedTick: -1,
	}})
	if err := protocol.ValidateReportAction(report); err == nil {
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
				Epoch:           validEpoch(),
				ObservationSeq:  1,
			})
		},
		"action_report": func() error {
			return protocol.ValidateReportAction(validTerminalReport(
				"report.metadata-positive",
				"event.metadata-positive",
				[]protocol.Fact{serverStampedFact},
			))
		},
		"batch": func() error {
			report := validTerminalReport(
				"batch.metadata",
				"event.metadata-batch",
				[]protocol.Fact{serverStampedFact},
			)
			return protocol.ValidateBatchActionReport(protocol.BatchActionReportRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.test",
				RequestID:       "batch.metadata",
				Reports:         []protocol.ActionReport{report.Report},
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
		DecisionWindow:  validWindow(),
		Offers: []protocol.ActionOffer{
			validOffer("talk"),
			validOffer("talk"),
		},
	}
	if err := protocol.ValidatePropose(request); err == nil {
		t.Fatal("duplicate action ids should fail")
	}
}

func TestLivingWorldRequestValidation(t *testing.T) {
	proposal := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.test", RequestID: "proposal.test", ActorID: "npc.test",
		Intent: "choose", DecisionWindow: validWindow(), Offers: []protocol.ActionOffer{validOffer("wait")},
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
	batch := protocol.BatchActionReportRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.test", RequestID: "batch.test",
		Reports: []protocol.ActionReport{{
			ProposalID: "proposal.one", EventID: "event.one",
			Decision: protocol.ActionAccepted, Summary: "Accepted without execution state.",
		}},
	}
	if err := protocol.ValidateBatchActionReport(batch); err == nil {
		t.Fatal("accepted batch item without outcome should fail")
	}
}

func TestEpochsMustBelongToRequestSession(t *testing.T) {
	t.Run("observe", func(t *testing.T) {
		request := protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       "session.test",
			RequestID:       "observe.cross-session",
			EventID:         "event.cross-session",
			ObserverIDs:     []string{"npc.test"},
			Source:          "game",
			Kind:            "world",
			Summary:         "An observation from the wrong Session.",
			Importance:      1,
			Epoch:           validEpoch(),
			ObservationSeq:  1,
		}
		request.Epoch.SessionID = "session.other"
		if err := protocol.ValidateObserve(request); err == nil {
			t.Fatal("cross-Session observation epoch was accepted")
		}
	})

	t.Run("propose", func(t *testing.T) {
		window := validWindow()
		window.Epoch.SessionID = "session.other"
		offer := validOffer("wait")
		offer.ExpectedEpoch = window.Epoch
		request := protocol.ProposeRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       "session.test",
			RequestID:       "proposal.cross-session",
			ActorID:         "npc.test",
			Intent:          "wait",
			DecisionWindow:  window,
			Offers:          []protocol.ActionOffer{offer},
		}
		if err := protocol.ValidatePropose(request); err == nil {
			t.Fatal("cross-Session decision window was accepted")
		}
	})

	t.Run("action report", func(t *testing.T) {
		request := validTerminalReport(
			"report.cross-session",
			"event.report.cross-session",
			nil,
		)
		request.Report.Invocation.ExpectedEpoch.SessionID = "session.other"
		request.Report.Outcome.Epoch.SessionID = "session.other"
		if err := protocol.ValidateReportAction(request); err == nil {
			t.Fatal("cross-Session action lifecycle was accepted")
		}
	})
}

func TestStructuredPayloadRejectsDuplicateNames(t *testing.T) {
	request := protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.test",
		RequestID:       "observe.duplicate-json",
		EventID:         "event.duplicate-json",
		ObserverIDs:     []string{"npc.test"},
		Source:          "game",
		Kind:            "world",
		Summary:         "Structured observation.",
		Importance:      1,
		Epoch:           validEpoch(),
		ObservationSeq:  1,
		Payload: &protocol.StructuredPayload{
			Schema: protocol.SchemaRef{
				ID:      "rin.test.observation",
				Version: "1.0.0",
				Digest:  strings.Repeat("a", 64),
			},
			Data: json.RawMessage(`{"state":"open","state":"closed"}`),
		},
	}
	if err := protocol.ValidateObserve(request); err == nil {
		t.Fatal("structured payload with duplicate names was accepted")
	}
}

func validCreate() protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.test",
		SessionID:       "session.test",
		Binding:         protocol.Binding{GameID: "game.test", ContentID: "base", ContentVersion: "1", ContentHash: "hash"},
		Features:        protocol.RecommendedFeatures(),
		Actors: []protocol.ActorSeed{{
			ID: "npc.test", Kind: "npc", DisplayName: "Test", ThinkEveryTicks: 1, Enabled: true,
			Boundaries: []protocol.Boundary{{ID: "boundary.test", Description: "A boundary", TriggerTags: []string{"private"}, Response: "refuse"}},
			Goals:      []protocol.Goal{{ID: "goal.test", Description: "A goal", Priority: 1, TargetProgress: 1, Status: "active"}},
		}},
	}
}

func validEpoch() protocol.Epoch {
	return protocol.Epoch{
		SessionID: "session.test",
		WorldID:   "world.test",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
}

func validWindow() protocol.DecisionWindow {
	return protocol.DecisionWindow{
		ID:             "window.test",
		Mode:           host.DecisionSequential,
		Epoch:          validEpoch(),
		ObservationSeq: 1,
		OpenedAt:       protocol.Timepoint{Clock: host.ClockStep, Value: 1},
		Deadline:       protocol.Timepoint{Clock: host.ClockStep, Value: 10},
		ActorIDs:       []string{"npc.test"},
	}
}

func validOffer(offerID string) protocol.ActionOffer {
	window := validWindow()
	return protocol.ActionOffer{
		OfferID:          offerID,
		DecisionWindowID: window.ID,
		ActorID:          "npc.test",
		Capability:       protocol.CapabilityRef{ID: "rin.test." + offerID, Version: "1.0.0"},
		DescriptorDigest: strings.Repeat("a", 64),
		Description:      "A bounded test offer.",
		Arguments:        json.RawMessage(`{}`),
		ExpectedEpoch:    window.Epoch,
		ObservationSeq:   window.ObservationSeq,
		Deadline:         window.Deadline,
	}
}

func validTerminalReport(
	requestID string,
	eventID string,
	facts []protocol.Fact,
) protocol.ReportActionRequest {
	offer := validOffer("wait")
	invocation := protocol.ActionInvocation{
		OperationID:      "operation.test",
		OfferID:          offer.OfferID,
		DecisionWindowID: offer.DecisionWindowID,
		ActorID:          offer.ActorID,
		Capability:       offer.Capability,
		DescriptorDigest: offer.DescriptorDigest,
		Arguments:        json.RawMessage(`{}`),
		ExpectedEpoch:    offer.ExpectedEpoch,
		ObservationSeq:   offer.ObservationSeq,
		Deadline:         offer.Deadline,
	}
	run := protocol.ActionRun{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		ProgressSeq: 1,
		Progress:    100,
		UpdatedAt:   protocol.Timepoint{Clock: host.ClockStep, Value: 2},
	}
	outcome := protocol.ActionOutcome{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     "Applied.",
		Epoch:       offer.ExpectedEpoch,
		WorldSeq:    1,
		OccurredAt:  protocol.Timepoint{Clock: host.ClockStep, Value: 2},
	}
	return protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.test",
		RequestID:       requestID,
		Tick:            2,
		Report: protocol.ActionReport{
			ProposalID: "proposal.test",
			EventID:    eventID,
			Decision:   protocol.ActionAccepted,
			Invocation: &invocation,
			Run:        &run,
			Outcome:    &outcome,
			Summary:    "Applied.",
			Facts:      facts,
		},
	}
}

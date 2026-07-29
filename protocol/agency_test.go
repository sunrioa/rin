package protocol

import "testing"

func TestResolveAgencyUsesMostRestrictiveLayers(t *testing.T) {
	actor := AgencyPolicy{
		Initiative: InitiativeDialogue, Obedience: ObedienceIndependent,
		MessageCooldownTicks: 600, MaxConsecutiveTurns: 4,
	}
	turn := AgencyTurn{
		Kind: TurnProactiveDialogue,
		HostCeiling: AgencyPolicy{
			Initiative: InitiativeActions, Obedience: ObedienceIndependent,
			MessageCooldownTicks: 200, MaxConsecutiveTurns: 8,
		},
		ServerPolicy: AgencyPolicy{
			Initiative: InitiativeDialogue, Obedience: ObedienceNegotiate,
			MessageCooldownTicks: 1200, MaxConsecutiveTurns: 2,
		},
	}
	decision := ResolveAgency(turn, actor)
	want := AgencyPolicy{
		Initiative: InitiativeDialogue, Obedience: ObedienceNegotiate,
		MessageCooldownTicks: 1200, MaxConsecutiveTurns: 2,
	}
	if decision.Effective != want {
		t.Fatalf("unexpected effective policy: %+v", decision.Effective)
	}
	if !AgencyAllowsInitiative(decision.Effective, InitiativeDialogue) ||
		AgencyAllowsInitiative(decision.Effective, InitiativeActions) {
		t.Fatal("initiative rank comparison is incorrect")
	}
}

func TestAgencyPolicyRejectsOutOfRangeValues(t *testing.T) {
	invalid := AgencyPolicy{
		Initiative: InitiativeActions, Obedience: ObedienceIndependent,
		MessageCooldownTicks: 1_000_001, MaxConsecutiveTurns: 9,
	}
	if err := ValidateAgencyPolicy("policy", invalid); err == nil {
		t.Fatal("invalid agency policy was accepted")
	}
	invalid = testAgencyPolicy()
	invalid.Initiative = "unbounded"
	if err := ValidateAgencyPolicy("policy", invalid); err == nil {
		t.Fatal("unknown initiative was accepted")
	}
}

func TestCreateSessionGatesActorAgencyByFeature(t *testing.T) {
	request := testAgencyCreateRequest()
	request.Actors[0].Agency = nil
	if err := ValidateCreateSession(request); err == nil {
		t.Fatal("agency feature accepted an actor without policy")
	}

	policy := testAgencyPolicy()
	request.Actors[0].Agency = &policy
	if err := ValidateCreateSession(request); err != nil {
		t.Fatalf("valid agency create request failed: %v", err)
	}

	request.Features = nil
	if err := ValidateCreateSession(request); err == nil {
		t.Fatal("actor agency was accepted without the feature")
	}
}

func TestValidateProposeChecksAgencyDirectiveOffers(t *testing.T) {
	request := testAgencyProposeRequest()
	if err := ValidatePropose(request); err != nil {
		t.Fatalf("valid agency turn failed: %v", err)
	}

	request.Agency.DirectiveOfferIDs = []string{"wait", "wait"}
	if err := ValidatePropose(request); err == nil {
		t.Fatal("duplicate directive offer ids were accepted")
	}
	request.Agency.DirectiveOfferIDs = []string{"missing"}
	if err := ValidatePropose(request); err == nil {
		t.Fatal("unknown directive offer id was accepted")
	}
	request.Agency.DirectiveOfferIDs = []string{"wait"}
	request.Agency.Directive = false
	if err := ValidatePropose(request); err == nil {
		t.Fatal("directive offer ids were accepted without a directive")
	}
}

func TestValidateSetActorAgencyRequiresUniqueBoundedUpdates(t *testing.T) {
	request := SetActorAgencyRequest{
		ProtocolVersion: Version,
		SessionID:       "session.test",
		RequestID:       "agency.update",
		Tick:            5,
		Updates: []ActorAgencyUpdate{{
			ActorID: "npc.test",
			Policy:  testAgencyPolicy(),
		}},
	}
	if err := ValidateSetActorAgency(request); err != nil {
		t.Fatalf("valid agency update failed: %v", err)
	}
	request.Updates = append(request.Updates, request.Updates[0])
	if err := ValidateSetActorAgency(request); err == nil {
		t.Fatal("duplicate actor update was accepted")
	}
	request.Updates = nil
	if err := ValidateSetActorAgency(request); err == nil {
		t.Fatal("empty actor update was accepted")
	}
}

func TestSessionStateGatesAgencyStateAndProposal(t *testing.T) {
	state := invariantTestState(FeatureActorAgency)
	state.WorldRevision = 1
	actor := state.Actors["npc.test"]
	policy := testAgencyPolicy()
	actor.Agency = &policy
	actor.AgencyState = &AgencyState{UpdatedRevision: 1}
	state.Actors[actor.ID] = actor
	if err := ValidateSessionState(state); err != nil {
		t.Fatalf("valid agency state failed: %v", err)
	}

	actor.AgencyState = nil
	state.Actors[actor.ID] = actor
	if err := ValidateSessionState(state); err == nil {
		t.Fatal("agency feature accepted missing agency state")
	}

	state = invariantTestState()
	actor = state.Actors["npc.test"]
	actor.Agency = &policy
	actor.AgencyState = &AgencyState{UpdatedRevision: 1}
	state.Actors[actor.ID] = actor
	if err := ValidateSessionState(state); err == nil {
		t.Fatal("agency state was accepted without the feature")
	}
}

func testAgencyPolicy() AgencyPolicy {
	return AgencyPolicy{
		Initiative: InitiativePassive, Obedience: ObedienceObey,
		MessageCooldownTicks: 1200, MaxConsecutiveTurns: 2,
	}
}

func testAgencyCreateRequest() CreateSessionRequest {
	return CreateSessionRequest{
		ProtocolVersion: Version,
		RequestID:       "create.agency",
		SessionID:       "session.agency",
		Binding: Binding{
			GameID: "game.test", ContentID: "content.test",
			ContentVersion: "1", ContentHash: "test",
		},
		Features: []string{FeatureActorAgency},
		Actors: []ActorSeed{{
			ID: "npc.test", Kind: "npc", DisplayName: "Test",
			ThinkEveryTicks: 20, Enabled: true,
		}},
	}
}

func testAgencyProposeRequest() ProposeRequest {
	return ProposeRequest{
		ProtocolVersion: Version,
		SessionID:       "session.test",
		RequestID:       "proposal.agency",
		ActorID:         "npc.test",
		Tick:            5,
		Intent:          "wait",
		DecisionWindow:  testDecisionWindow("session.test", "npc.test", 1),
		Offers:          []ActionOffer{testActionOffer("session.test", "npc.test", "wait", 1)},
		Agency: &AgencyTurn{
			Kind:              TurnResponsive,
			Directive:         true,
			DirectiveOfferIDs: []string{"wait"},
			HostCeiling:       testAgencyPolicy(),
			ServerPolicy:      testAgencyPolicy(),
		},
	}
}

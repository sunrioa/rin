package policy_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestDeterministicPolicyUsesGoalAndMemory(t *testing.T) {
	input := policyInput()
	draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.OfferID != "talk" || draft.GoalID != "goal.connect" {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	if len(draft.RecalledMemoryIDs) != 2 || draft.RecalledMemoryIDs[0] != "memory.relevant" {
		t.Fatalf("unexpected recall order: %v", draft.RecalledMemoryIDs)
	}
	repeated, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil || !reflect.DeepEqual(repeated, draft) {
		t.Fatalf("policy should be deterministic: first=%+v second=%+v err=%v", draft, repeated, err)
	}
}

func TestDeterministicPolicyLetsRecalledMemoryInfluenceAction(t *testing.T) {
	input := policyInput()
	input.Actor.Goals = nil
	input.Request.Tags = nil
	input.Request.Offers = []protocol.ActionOffer{
		policyOffer("offer.coffee", "coffee", "Offer coffee."),
		policyOffer("offer.tea", "tea", "Offer tea."),
	}
	input.Actor.Memories = []protocol.Memory{{
		ID: "memory.preference", EventID: "event.preference", Tick: 4,
		Summary: "The player chose tea.", Tags: []string{"tea"}, Importance: 4,
	}}

	draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.OfferID != "offer.tea" {
		t.Fatalf("recalled preference did not influence the allowlisted action: %+v", draft)
	}
	if len(draft.RecalledMemoryIDs) != 1 || draft.RecalledMemoryIDs[0] != "memory.preference" {
		t.Fatalf("unexpected recall evidence: %+v", draft)
	}
}

func TestDeterministicPolicyDoesNotScoreUnrecalledMemory(t *testing.T) {
	input := policyInput()
	input.Actor.Goals = nil
	input.Request.Tags = nil
	input.Request.Offers = []protocol.ActionOffer{
		policyOffer("offer.coffee", "coffee", "Offer coffee."),
		policyOffer("offer.tea", "tea", "Offer tea."),
	}
	input.Actor.Memories = []protocol.Memory{
		{
			ID: "memory.important", EventID: "event.important", Tick: 5,
			Summary: "A recent event.", Tags: []string{"weather"}, Importance: 5,
		},
		{
			ID: "memory.preference", EventID: "event.preference", Tick: 1,
			Summary: "The player chose tea.", Tags: []string{"tea"}, Importance: 1,
		},
	}

	draft, err := (policy.Deterministic{MemoryLimit: 1}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.RecalledMemoryIDs) != 1 || draft.RecalledMemoryIDs[0] != "memory.important" {
		t.Fatalf("unexpected bounded recall: %+v", draft)
	}
	input.Actor.Memories = input.Actor.Memories[:1]
	withoutUnrecalled, err := (policy.Deterministic{MemoryLimit: 1}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.OfferID != withoutUnrecalled.OfferID {
		t.Fatalf("an unrecalled memory changed the action: with=%+v without=%+v", draft, withoutUnrecalled)
	}
}

func TestDeterministicPolicyProtectsBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		action protocol.ActionOffer
	}{
		{
			name:   "response matches action id",
			action: policyOffer("refuse", "dialogue", "decline safely"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := policyInput()
			input.Request.Tags = []string{"private"}
			input.Request.Offers = []protocol.ActionOffer{test.action}
			draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if draft.OfferID != test.action.OfferID ||
				draft.Stance != "refuse" ||
				draft.BoundaryID != "boundary.private" {
				t.Fatalf("unexpected boundary draft: %+v", draft)
			}
		})
	}

	input := policyInput()
	input.Request.Tags = []string{"private"}
	input.Request.Offers = input.Request.Offers[:1]
	if _, err := (policy.Deterministic{}).Propose(context.Background(), input); !errors.Is(err, rinruntime.ErrNoSafeAction) {
		t.Fatalf("expected no safe action, got %v", err)
	}
}

func TestDeterministicPolicyObeysBoundDirectiveOffer(t *testing.T) {
	input := policyInput()
	input.Agency = obeyDirective("wait")
	draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil || draft.OfferID != "wait" {
		t.Fatalf("directive was not obeyed: %+v err=%v", draft, err)
	}
}

func TestDeterministicPolicyKeepsBoundaryAheadOfDirective(t *testing.T) {
	input := policyInput()
	input.Request.Tags = []string{"private"}
	input.Agency = obeyDirective("talk")
	draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.OfferID != "refuse" || draft.BoundaryID != "boundary.private" {
		t.Fatalf("directive bypassed actor boundary: %+v", draft)
	}
}

func TestDeterministicDraftKeepsOnlyStructuredPrivateAuditEvidence(t *testing.T) {
	const canary = "PRIVATE_DECISION_CANARY_31B9"
	input := policyInput()
	input.Actor.DisplayName = canary
	input.Actor.Boundaries[0].Description = canary
	input.Actor.Goals[0].Description = canary
	input.Actor.Memories[0].Summary = canary
	input.Actor.Memories[1].Summary = canary
	input.Actor.Memories[1].Quote = canary
	input.Request.Tags = []string{"private"}
	draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.BoundaryID != "boundary.private" || len(draft.RecalledMemoryIDs) == 0 {
		t.Fatalf("structured private audit evidence was lost: %+v", draft)
	}
}

func TestDeterministicPolicyHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (policy.Deterministic{}).Propose(ctx, policyInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled policy, got %v", err)
	}
}

func policyInput() rinruntime.DecisionContext {
	actor := protocol.ActorState{
		ActorSeed: protocol.ActorSeed{
			ID: "npc.mira", Kind: "npc", DisplayName: "Mira", Enabled: true, ThinkEveryTicks: 5,
			Boundaries: []protocol.Boundary{{ID: "boundary.private", Description: "Keep letters private.", TriggerTags: []string{"private"}, Response: "refuse"}},
			Goals:      []protocol.Goal{{ID: "goal.connect", Description: "Build trust.", Priority: 4, PreferredActions: []string{"talk"}, TargetProgress: 3, Status: "active"}},
		},
		Memories: []protocol.Memory{
			{ID: "memory.old", EventID: "event.old", Tick: 1, Summary: "An old event.", Tags: []string{"weather"}, Importance: 2},
			{ID: "memory.relevant", EventID: "event.relevant", Tick: 5, Summary: "The player waited.", Quote: "Take your time.", Tags: []string{"trust"}, Importance: 4},
		},
	}
	request := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.policy", RequestID: "request.policy", ActorID: actor.ID,
		Tick: 6, Intent: "Respond", Tags: []string{"trust"},
		Offers: []protocol.ActionOffer{
			policyOffer("talk", "dialogue", "ask a question"),
			policyOffer("refuse", "refuse", "protect a boundary"),
			policyOffer("wait", "wait", "wait"),
		},
	}
	return rinruntime.DecisionContext{
		State: protocol.SessionState{ProtocolVersion: protocol.Version, SessionID: "session.policy", Seed: 42},
		Actor: actor, Request: request,
	}
}

func policyOffer(id, capability, description string) protocol.ActionOffer {
	return protocol.ActionOffer{
		OfferID: id,
		Capability: protocol.CapabilityRef{
			ID:      capability,
			Version: "1.0.0",
		},
		Description: description,
	}
}

func obeyDirective(offerIDs ...string) *protocol.AgencyDecision {
	return &protocol.AgencyDecision{
		Kind:              protocol.TurnResponsive,
		Directive:         true,
		DirectiveOfferIDs: append([]string(nil), offerIDs...),
		Effective: protocol.AgencyPolicy{
			Initiative:           protocol.InitiativePassive,
			Obedience:            protocol.ObedienceObey,
			MessageCooldownTicks: 1200,
			MaxConsecutiveTurns:  2,
		},
	}
}

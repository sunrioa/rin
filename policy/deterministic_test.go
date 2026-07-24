package policy_test

import (
	"context"
	"errors"
	"strings"
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
	if draft.ActionID != "talk" || draft.GoalID != "goal.connect" {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	if len(draft.RecalledMemoryIDs) != 2 || draft.RecalledMemoryIDs[0] != "memory.relevant" {
		t.Fatalf("unexpected recall order: %v", draft.RecalledMemoryIDs)
	}
	repeated, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil || repeated.ActionID != draft.ActionID || repeated.Rationale != draft.Rationale {
		t.Fatalf("policy should be deterministic: first=%+v second=%+v err=%v", draft, repeated, err)
	}
}

func TestDeterministicPolicyProtectsBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		action protocol.ActionSpec
	}{
		{
			name: "response matches action id",
			action: protocol.ActionSpec{
				ID: "refuse", Kind: "dialogue", Description: "decline safely",
			},
		},
		{
			name: "response matches action kind",
			action: protocol.ActionSpec{
				ID: "decline", Kind: "refuse", Description: "decline safely",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := policyInput()
			input.Request.Tags = []string{"private"}
			input.Request.CandidateActions = []protocol.ActionSpec{test.action}
			draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if draft.ActionID != test.action.ID ||
				draft.Stance != "refuse" ||
				draft.BoundaryID != "boundary.private" {
				t.Fatalf("unexpected boundary draft: %+v", draft)
			}
		})
	}

	input := policyInput()
	input.Request.Tags = []string{"private"}
	input.Request.CandidateActions = input.Request.CandidateActions[:1]
	if _, err := (policy.Deterministic{}).Propose(context.Background(), input); !errors.Is(err, rinruntime.ErrNoSafeAction) {
		t.Fatalf("expected no safe action, got %v", err)
	}
}

func TestDeterministicPlayerTextDoesNotExposePrivateDecisionContext(t *testing.T) {
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
	if draft.Summary != "" || draft.Rationale != "" ||
		strings.Contains(draft.Summary, canary) || strings.Contains(draft.Rationale, canary) {
		t.Fatalf("private context reached player-facing text: %+v", draft)
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

func policyInput() rinruntime.PolicyContext {
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
		CandidateActions: []protocol.ActionSpec{
			{ID: "talk", Kind: "dialogue", Description: "ask a question"},
			{ID: "refuse", Kind: "refuse", Description: "protect a boundary"},
			{ID: "wait", Kind: "wait", Description: "wait"},
		},
	}
	return rinruntime.PolicyContext{
		State: protocol.SessionState{ProtocolVersion: protocol.Version, SessionID: "session.policy", Seed: 42},
		Actor: actor, Request: request,
	}
}
